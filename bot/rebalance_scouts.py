#!/usr/bin/env python3
"""
Scout Rebalancing Script for X1-GH18

Generates balanced, disjoint market assignments for 11 scouts.
"""

import json
import sys
from typing import Dict, List, Tuple
import math

# All 27 unique markets from log analysis
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

def kmeans_clustering(coords: Dict[str, Tuple[int, int]], k: int, max_iterations: int = 50) -> List[List[str]]:
    """
    K-means clustering to partition markets into k balanced groups

    Returns: List of k market lists (clusters)
    """
    markets = list(coords.keys())
    positions = [coords[m] for m in markets]

    # Initialize centroids using k-means++ algorithm
    import random
    random.seed(42)  # Deterministic

    centroids = []
    # First centroid: random market
    first_idx = random.randint(0, len(markets) - 1)
    centroids.append(positions[first_idx])

    # Remaining centroids: choose farthest from existing
    for _ in range(k - 1):
        max_min_dist = -1
        best_idx = 0

        for i, pos in enumerate(positions):
            min_dist = min(distance(pos, c) for c in centroids)
            if min_dist > max_min_dist:
                max_min_dist = min_dist
                best_idx = i

        centroids.append(positions[best_idx])

    # K-means iteration
    for iteration in range(max_iterations):
        # Assign each market to nearest centroid
        clusters = [[] for _ in range(k)]

        for i, pos in enumerate(positions):
            nearest = min(range(k), key=lambda c: distance(pos, centroids[c]))
            clusters[nearest].append(i)

        # Recalculate centroids
        new_centroids = []
        for cluster in clusters:
            if cluster:
                avg_x = sum(positions[i][0] for i in cluster) / len(cluster)
                avg_y = sum(positions[i][1] for i in cluster) / len(cluster)
                new_centroids.append((avg_x, avg_y))
            else:
                # Empty cluster: keep old centroid
                new_centroids.append(centroids[len(new_centroids)])

        # Check convergence
        if all(distance(old, new) < 1.0 for old, new in zip(centroids, new_centroids)):
            print(f"K-means converged in {iteration + 1} iterations")
            break

        centroids = new_centroids

    # Convert cluster indices to market names
    result = [[markets[i] for i in cluster] for cluster in clusters]

    return result

def estimate_tour_time(markets: List[str], coords: Dict[str, Tuple[int, int]]) -> float:
    """
    Estimate tour time using nearest-neighbor TSP approximation

    Formula: (distance * 26 / 9) + markets * 22
    """
    if not markets:
        return 0.0

    if len(markets) == 1:
        return 22.0  # Just overhead

    market_coords = [coords[m] for m in markets if m in coords]

    if len(market_coords) == 1:
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

    # SpaceTraders formula: round((distance * 26) / 9) + overhead
    travel_time = round((total_distance * 26) / 9)
    overhead = len(markets) * 22

    return travel_time + overhead

def balance_clusters(clusters: List[List[str]], coords: Dict[str, Tuple[int, int]],
                    max_iterations: int = 100, target_variance: float = 0.20, min_markets: int = 2) -> List[List[str]]:
    """
    Balance tour times across clusters by moving markets between adjacent clusters
    """
    print(f"\nBalancing tour times (target variance: {target_variance*100:.0f}%, min {min_markets} markets/scout)...")

    # PHASE 1: Ensure minimum markets per cluster
    print("\nPhase 1: Ensuring minimum markets per scout...")
    for iteration in range(max_iterations):
        # Find clusters below minimum
        poor_clusters = [i for i, c in enumerate(clusters) if len(c) < min_markets]

        if not poor_clusters:
            print(f"  All clusters have at least {min_markets} markets")
            break

        # Find richest cluster
        rich_idx = max(range(len(clusters)), key=lambda i: len(clusters[i]))

        if len(clusters[rich_idx]) <= min_markets:
            print(f"  Cannot ensure minimum: richest cluster has only {len(clusters[rich_idx])} markets")
            break

        # Move market from rich to poorest
        poor_idx = min(poor_clusters, key=lambda i: len(clusters[i]))

        # Find market in rich cluster closest to poor cluster
        if not clusters[poor_idx]:
            market_to_move = clusters[rich_idx][0]
        else:
            poor_coords = [coords[m] for m in clusters[poor_idx] if m in coords]
            if poor_coords:
                centroid_x = sum(p[0] for p in poor_coords) / len(poor_coords)
                centroid_y = sum(p[1] for p in poor_coords) / len(poor_coords)

                market_to_move = min(
                    clusters[rich_idx],
                    key=lambda m: distance(coords[m], (centroid_x, centroid_y)) if m in coords else float('inf')
                )
            else:
                market_to_move = clusters[rich_idx][0]

        clusters[rich_idx].remove(market_to_move)
        clusters[poor_idx].append(market_to_move)

        print(f"  Moved {market_to_move} from cluster {rich_idx} to {poor_idx} (now {len(clusters[poor_idx])} markets)")

    # PHASE 2: Balance tour times
    print("\nPhase 2: Balancing tour times...")
    last_moved = None

    for iteration in range(max_iterations):
        # Calculate tour times
        tour_times = [estimate_tour_time(cluster, coords) for cluster in clusters]

        # Calculate variance
        if not any(tour_times):
            break

        avg_time = sum(tour_times) / len([t for t in tour_times if t > 0])
        variance = max(abs(t - avg_time) / avg_time for t in tour_times if t > 0)

        print(f"  Iteration {iteration + 1}: variance = {variance*100:.1f}%")

        if variance < target_variance:
            print(f"  Converged! Variance below {target_variance*100:.0f}%")
            break

        # Find longest and shortest tours
        longest_idx = max(range(len(tour_times)), key=lambda i: tour_times[i])
        shortest_idx = min(range(len(tour_times)), key=lambda i: tour_times[i] if len(clusters[i]) < len(ALL_MARKETS) else float('inf'))

        # Don't reduce below min_markets
        if len(clusters[longest_idx]) <= min_markets:
            print(f"  Cannot balance further (longest cluster has only {min_markets} markets)")
            break

        # Find market in longest cluster closest to shortest cluster
        longest_cluster = clusters[longest_idx]
        shortest_cluster = clusters[shortest_idx]

        if not shortest_cluster:
            # Just take first market
            market_to_move = longest_cluster[0]
        else:
            # Calculate centroid of shortest cluster
            shortest_coords = [coords[m] for m in shortest_cluster if m in coords]
            if shortest_coords:
                centroid_x = sum(p[0] for p in shortest_coords) / len(shortest_coords)
                centroid_y = sum(p[1] for p in shortest_coords) / len(shortest_coords)

                # Find closest market in longest cluster
                market_to_move = min(
                    longest_cluster,
                    key=lambda m: distance(coords[m], (centroid_x, centroid_y)) if m in coords else float('inf')
                )
            else:
                market_to_move = longest_cluster[0]

        # Check for oscillation
        if market_to_move == last_moved:
            print(f"  Detected oscillation, stopping")
            break

        # Move market
        clusters[longest_idx].remove(market_to_move)
        clusters[shortest_idx].append(market_to_move)
        last_moved = market_to_move

        print(f"  Moved {market_to_move} from cluster {longest_idx} (now {len(clusters[longest_idx])} markets) to {shortest_idx} (now {len(clusters[shortest_idx])} markets)")

    return clusters

def validate_partitions(partitions: Dict[str, List[str]]) -> bool:
    """
    Validate that partitions are disjoint (no overlaps)

    Returns: True if valid, False if overlaps detected
    """
    all_markets = []
    for markets in partitions.values():
        all_markets.extend(markets)

    # Check for duplicates
    if len(all_markets) != len(set(all_markets)):
        print("\n❌ VALIDATION FAILED: Overlapping markets detected!")

        # Find duplicates
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

def main():
    # Load graph data
    print("Loading graph data...")
    graph_file = "/Users/andres.camacho/Development/Personal/spacetradersV2/bot/var/data/graphs/X1-GH18_graph.json"

    try:
        with open(graph_file, 'r') as f:
            graph_data = json.load(f)
    except FileNotFoundError:
        print(f"❌ Graph file not found: {graph_file}")
        print("   Please build graph first using: python3 spacetraders_bot.py graph build --system X1-GH18")
        return 1

    coords = get_market_coordinates(graph_data)
    print(f"✅ Loaded coordinates for {len(coords)} markets")

    # Verify all markets have coordinates
    missing_coords = [m for m in ALL_MARKETS if m not in coords]
    if missing_coords:
        print(f"⚠️  WARNING: Missing coordinates for: {missing_coords}")

    # K-means clustering
    print(f"\nClustering {len(ALL_MARKETS)} markets into {len(SHIPS)} groups...")
    clusters = kmeans_clustering(coords, len(SHIPS))

    # Balance tour times
    balanced_clusters = balance_clusters(clusters, coords)

    # Create partitions dict
    partitions = {}
    for i, ship in enumerate(SHIPS):
        if i < len(balanced_clusters):
            partitions[ship] = sorted(balanced_clusters[i])
        else:
            partitions[ship] = []

    # Validate
    if not validate_partitions(partitions):
        return 1

    # Print final assignments
    print("\n" + "="*70)
    print("FINAL BALANCED TOUR ASSIGNMENTS")
    print("="*70)

    total_time = 0
    for ship in SHIPS:
        markets = partitions[ship]
        tour_time = estimate_tour_time(markets, coords)
        total_time += tour_time
        time_min = tour_time / 60

        print(f"{ship}: {len(markets):2d} markets, {time_min:6.1f} min")
        print(f"  Markets: {', '.join(markets)}")

    avg_time = total_time / len(SHIPS) / 60
    print(f"\nAverage tour time: {avg_time:.1f} min")
    print("="*70 + "\n")

    # Save config
    output_file = "/Users/andres.camacho/Development/Personal/spacetradersV2/bot/config/agents/scout_partitions_X1-GH18.json"

    config = {
        "system": "X1-GH18",
        "ships": SHIPS,
        "partitions": partitions,
        "algorithm": "2opt",
        "generated_by": "rebalance_scouts.py",
        "validation": "strict_disjoint",
        "timestamp": "2025-10-09T11:00:00Z"
    }

    with open(output_file, 'w') as f:
        json.dump(config, f, indent=2)

    print(f"💾 Saved partitions to: {output_file}\n")

    return 0

if __name__ == '__main__':
    sys.exit(main())
