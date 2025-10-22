#!/usr/bin/env python3
"""
Scout Coordinator - Multi-ship continuous market scouting

Manages multiple scout ships with non-overlapping subtours for
continuous market intelligence gathering.
"""

import json
import signal
import time
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Dict, List, Optional, Set, Tuple

from ..helpers import paths
from .api_client import APIClient
from .ship_assignment_repository import AssignmentManager
from .daemon_manager import DaemonManager
from .market_partitioning import MarketPartitioner
from .route_planner import TourOptimizer
from .system_graph_provider import SystemGraphProvider
from .balance_oscillation_detector import BalanceOscillationDetector
from .dispersed_pair_handler import DispersedPairHandler


@dataclass
class SubtourAssignment:
    """Assignment of markets to a ship"""
    ship: str
    markets: List[str]
    tour_time_seconds: float
    daemon_id: str
@dataclass
class DaemonHealth:
    """Represents the current running status of a scout daemon."""

    ship: str
    daemon_id: str
    is_running: bool


class ScoutCoordinator:
    """
    Coordinates multiple scout ships for continuous market scouting

    Features:
    - Partitions markets into non-overlapping subtours
    - Balances tour times across ships (not just market count)
    - Continuous operation (restarts immediately after completion)
    - Graceful reconfiguration (add/remove ships without data gaps)
    - Monitors scout health and restarts on failure
    """

    def __init__(self, system: str, ships: List[str], token: str, player_id: int,
                 config_file: Optional[str] = None,
                 graph_provider: Optional[SystemGraphProvider] = None,
                 exclude_markets: Optional[List[str]] = None):
        """
        Initialize scout coordinator

        Args:
            system: System symbol (e.g., X1-HU87)
            ships: List of ship symbols
            token: Agent token
            player_id: Player ID for daemon management
            config_file: Optional config file path for reconfiguration
            graph_provider: Optional SystemGraphProvider for testing
            exclude_markets: Optional list of market waypoints to exclude from auto-discovery
                           (e.g., ['X1-TX46-I52', 'X1-TX46-J55'] for stationary scouts)
        """
        self.system = system
        self.ships = set(ships)
        self.token = token
        self.player_id = player_id
        self.exclude_markets = set(exclude_markets) if exclude_markets else set()
        paths.ensure_dirs((paths.AGENT_CONFIG_DIR,))
        self.config_file = Path(config_file) if config_file else paths.AGENT_CONFIG_DIR / f"scout_config_{system}.json"

        self.api = APIClient(token)
        self.daemon_manager = DaemonManager(player_id)
        self.assignment_manager = AssignmentManager(player_id=player_id)
        self.graph_provider = graph_provider or SystemGraphProvider(self.api)

        # State
        self.graph = None
        self.markets = []
        self.assignments: Dict[str, SubtourAssignment] = {}
        self.running = True
        self.reconfigure_requested = False
        self._partitioner: Optional[MarketPartitioner] = None
        self._ship_data_cache: Dict[str, Dict] = {}

        # Load graph
        self._load_or_build_graph()

        # Initialize helper classes (after graph is loaded)
        self._oscillation_detector = BalanceOscillationDetector(self._find_boundary_market)
        self._dispersed_pair_handler = DispersedPairHandler(self.graph, self.markets)

        # Setup signal handlers
        signal.signal(signal.SIGTERM, self._handle_signal)
        signal.signal(signal.SIGINT, self._handle_signal)

    def _handle_signal(self, signum, frame):
        """Handle shutdown signals gracefully"""
        print(f"\n⚠️  Received signal {signum}, shutting down gracefully...")
        self.running = False

    def _load_or_build_graph(self):
        """Load or build navigation graph"""
        result = self.graph_provider.get_graph(self.system)
        self.graph = result.graph
        if result.message:
            print(result.message)

        # Extract markets
        all_markets = TourOptimizer.get_markets_from_graph(self.graph)

        # Filter out excluded markets (for stationary scouts with dedicated polling)
        if self.exclude_markets:
            self.markets = [m for m in all_markets if m not in self.exclude_markets]
            excluded_list = ', '.join(sorted(self.exclude_markets))
            print(f"✅ Found {len(all_markets)} markets, excluding {len(self.exclude_markets)} ({excluded_list}): {len(self.markets)} markets assigned to touring scouts")
        else:
            self.markets = all_markets
            print(f"✅ Found {len(self.markets)} markets: {', '.join(self.markets)}")

        self._invalidate_partitioner()

    def partition_markets_greedy(self) -> Dict[str, List[str]]:
        """Assign markets greedily based on current tour time estimates."""

        result = self._create_partitioner().partition("greedy")
        if result.message:
            print(f"\n{result.message}")
        return result.partitions

    def partition_markets_kmeans(self) -> Dict[str, List[str]]:
        """Cluster markets using K-means to form compact tours."""

        result = self._create_partitioner().partition("kmeans")
        if result.message:
            print(f"\n{result.message}")
        return result.partitions

    def partition_markets_geographic(self) -> Dict[str, List[str]]:
        """Slice markets geographically to generate initial assignments."""

        result = self._create_partitioner().partition("geographic")
        return result.partitions

    def partition_markets_ortools(self) -> Dict[str, List[str]]:
        """Use OR-Tools multi-vehicle optimisation to partition markets."""

        result = self._create_partitioner().partition("ortools")
        if result.message:
            print(f"\n{result.message}")
        return result.partitions

    def _create_partitioner(self) -> MarketPartitioner:
        if self._partitioner is None:
            self._partitioner = MarketPartitioner(
                graph=self.graph or {},
                markets=self.markets,
                ships=sorted(self.ships),
                ship_data=self._get_ship_data_map(),
            )
        return self._partitioner

    def _get_ship_data_map(self) -> Dict[str, Dict]:
        """Fetch and cache ship data required for OR-Tools partitioning."""
        updated: Dict[str, Dict] = {}
        for ship in self.ships:
            if ship not in self._ship_data_cache:
                try:
                    self._ship_data_cache[ship] = self.api.get_ship(ship)
                except Exception as exc:  # broad catch to avoid breaking coordinator
                    print(f"⚠️  Failed to load ship data for {ship}: {exc}")
                    continue
            updated[ship] = self._ship_data_cache[ship]
        return updated

    def _invalidate_partitioner(self) -> None:
        self._partitioner = None

    def balance_tour_times(self, partitions: Dict[str, List[str]],
                          max_iterations: int = 50,
                          variance_threshold: float = 0.3,
                          min_markets: int = 1,
                          use_tsp: bool = True) -> Dict[str, List[str]]:
        """
        Rebalance partitions to equalize tour times across ships

        Args:
            partitions: Initial market partitions
            max_iterations: Maximum rebalancing iterations
            variance_threshold: Stop if tour time variance < this (0.3 = 30%)
            min_markets: Minimum markets per scout (default: 1, allows stationary scouts)

        Returns:
            Rebalanced partitions with roughly equal tour times
        """
        print(f"\n⚖️  Balancing tour times (target variance: {variance_threshold*100:.0f}%, min {min_markets} markets/scout)...")

        # Calculate tour times for each partition
        tour_times = {}
        # Pre-load all ship data to avoid cache misses later
        ship_data_cache = self._get_ship_data_map()

        for ship, markets in partitions.items():
            if not markets:
                tour_times[ship] = 0.0
                continue

            ship_data = ship_data_cache.get(ship)
            if not ship_data:
                print(f"⚠️  Could not get data for {ship}, skipping")
                tour_times[ship] = 0.0
                continue

            # Calculate tour time - use fast estimate by default, TSP if requested
            if use_tsp:
                tour_time = self._calculate_partition_tour_time(markets, ship_data)
            else:
                tour_time = self._estimate_partition_tour_time(markets, ship_data)
            tour_times[ship] = tour_time

        # Print initial distribution
        print("\nInitial tour times:")
        for ship in sorted(partitions.keys()):
            markets_count = len(partitions[ship])
            time_min = tour_times[ship] / 60
            print(f"  {ship}: {markets_count:2d} markets, {time_min:6.1f} min")

        # PRE-BALANCING: Ensure all scouts have at least min_markets and not all co-located
        print(f"\n🔧 Pre-balancing: Ensuring all scouts have at least {min_markets} markets and diverse coordinates...")

        for ship in sorted(partitions.keys()):
            # First, ensure minimum market count
            while len(partitions[ship]) < min_markets:
                # Find scout with most markets
                richest_ship = max(partitions.keys(), key=lambda s: len(partitions[s]))

                if len(partitions[richest_ship]) <= min_markets:
                    print(f"⚠️  Cannot ensure minimum: richest scout has only {len(partitions[richest_ship])} markets")
                    break

                market_to_move = self._find_boundary_market(partitions[richest_ship], partitions[ship])

                if not market_to_move:
                    print(f"⚠️  No suitable market to move to {ship}")
                    break

                partitions[richest_ship].remove(market_to_move)
                partitions[ship].append(market_to_move)
                print(f"  Moved {market_to_move} from {richest_ship} to {ship} ({len(partitions[ship])} markets)")

            # Then, ensure not all markets are co-located (all same coordinates)
            # Only check for diversity if scout has 2+ markets (single market scouts are stationary by design)
            if len(partitions[ship]) >= 2:
                current_coords = set()
                for m in partitions[ship]:
                    wp = self.graph['waypoints'].get(m)
                    if wp:
                        current_coords.add((wp['x'], wp['y']))

                # If all markets at same coordinates, add one from different location
                if len(current_coords) == 1:
                    print(f"  ⚠️  {ship} has all markets co-located, adding diverse market...")
                    richest_ship = max(partitions.keys(), key=lambda s: len(partitions[s]))

                    # Find market with different coordinates
                    market_to_move = None
                    for m in partitions[richest_ship]:
                        wp = self.graph['waypoints'].get(m)
                        if wp and (wp['x'], wp['y']) not in current_coords:
                            market_to_move = m
                            break

                    if market_to_move:
                        partitions[richest_ship].remove(market_to_move)
                        partitions[ship].append(market_to_move)
                        print(f"  Moved {market_to_move} from {richest_ship} to {ship} (now {len(partitions[ship])} markets with {len(current_coords)+1} unique locations)")

        # Recalculate tour times after pre-balancing (use same method as initial)
        for ship, markets in partitions.items():
            if markets:
                ship_data = ship_data_cache[ship]
                if use_tsp:
                    tour_times[ship] = self._calculate_partition_tour_time(markets, ship_data)
                else:
                    tour_times[ship] = self._estimate_partition_tour_time(markets, ship_data)
            else:
                tour_times[ship] = 0.0

        print("\nAfter pre-balancing:")
        for ship in sorted(partitions.keys()):
            markets_count = len(partitions[ship])
            time_min = tour_times[ship] / 60
            print(f"  {ship}: {markets_count:2d} markets, {time_min:6.1f} min")

        # Iterative rebalancing
        last_moved = None  # Track last moved market to detect oscillations
        for iteration in range(max_iterations):
            # Calculate variance
            times = [t for t in tour_times.values() if t > 0]
            if not times:
                break

            avg_time = sum(times) / len(times)
            variance = max(abs(t - avg_time) / avg_time for t in times) if avg_time > 0 else 0

            print(f"\nIteration {iteration + 1}: variance = {variance*100:.1f}%")

            if variance < variance_threshold:
                print(f"✅ Converged! Variance below {variance_threshold*100:.0f}%")
                break

            # Find ship with longest tour and ship with shortest tour
            longest_ship = max(tour_times.keys(), key=lambda s: tour_times[s])
            shortest_ship = min(tour_times.keys(), key=lambda s: tour_times[s] if len(partitions[s]) < len(self.markets) else float('inf'))

            # HARD CONSTRAINT: Never reduce below min_markets
            # This ensures all ships are utilized for maximum data freshness
            if len(partitions[longest_ship]) <= min_markets:
                print(f"⚠️  Cannot rebalance further: {longest_ship} has minimum {min_markets} markets")
                print(f"   Current variance: {variance*100:.1f}% (prioritizing ship utilization over perfect balance)")
                break

            # Find market in longest tour that's closest to shortest ship's region
            # IMPROVEMENT: If tour time is extremely high, prioritize moving markets that
            # contribute most to the tour distance (outliers in dispersed clusters)
            # Use 1.5x threshold to catch cases just below 2x (e.g., 681.6 vs 345.7*2=691.4)
            if tour_times[longest_ship] > avg_time * 1.5:  # If 1.5x longer than average
                # Find the market that contributes most to tour time
                market_to_move = self._find_most_expensive_market(partitions[longest_ship])
                print(f"   Extreme imbalance detected ({tour_times[longest_ship]/60:.1f} min vs avg {avg_time/60:.1f} min)")
                print(f"   Moving most expensive market: {market_to_move}")
            else:
                market_to_move = self._find_boundary_market(
                    partitions[longest_ship],
                    partitions[shortest_ship]
                )

            if not market_to_move:
                print("⚠️  No suitable market to move")
                break

            # Detect oscillation: don't move a market that was just moved
            market_to_move = self._oscillation_detector.check_and_resolve(
                market_to_move,
                last_moved,
                partitions[longest_ship],
                partitions[shortest_ship]
            )
            if market_to_move is None:
                break

            # PREVIEW: Simulate the move to check if it improves variance
            # Make a copy of tour times to test the move
            preview_times = tour_times.copy()

            # Calculate what tour times would be after the move
            temp_longest = partitions[longest_ship].copy()
            temp_shortest = partitions[shortest_ship].copy()
            temp_longest.remove(market_to_move)
            temp_shortest.append(market_to_move)

            ship_data = ship_data_cache[longest_ship]
            if use_tsp:
                preview_times[longest_ship] = self._calculate_partition_tour_time(temp_longest, ship_data) if temp_longest else 0.0
            else:
                preview_times[longest_ship] = self._estimate_partition_tour_time(temp_longest, ship_data) if temp_longest else 0.0

            ship_data = ship_data_cache[shortest_ship]
            if use_tsp:
                preview_times[shortest_ship] = self._calculate_partition_tour_time(temp_shortest, ship_data) if temp_shortest else 0.0
            else:
                preview_times[shortest_ship] = self._estimate_partition_tour_time(temp_shortest, ship_data) if temp_shortest else 0.0

            # Calculate new variance
            preview_values = [t for t in preview_times.values() if t > 0]
            if not preview_values:
                print("⚠️  Preview move would result in no valid tours, stopping")
                break

            preview_avg = sum(preview_values) / len(preview_values)
            preview_variance = max(abs(t - preview_avg) / preview_avg for t in preview_values) if preview_avg > 0 else 0

            # Check if move would make variance worse
            # Allow slight increases (<10%) to escape local minima, but only if variance is still very high (>50%)
            variance_increase = preview_variance - variance
            variance_increase_pct = (variance_increase / variance * 100) if variance > 0 else 0

            if preview_variance > variance:
                # Allow small increases if we're far from target
                if variance > 0.5 and variance_increase_pct < 10:
                    print(f"   ⚠️  Accepting small variance increase ({variance*100:.1f}% → {preview_variance*100:.1f}%) to escape local minimum")
                else:
                    print(f"   ⚠️  Move would increase variance from {variance*100:.1f}% to {preview_variance*100:.1f}%, rejecting")
                    # If we can't improve further, stop
                    print("   No beneficial moves available, stopping")
                    break

            # Move is beneficial, execute it
            partitions[longest_ship].remove(market_to_move)
            partitions[shortest_ship].append(market_to_move)
            last_moved = market_to_move  # Track for oscillation detection

            # Update tour times with the previewed values
            tour_times[longest_ship] = preview_times[longest_ship]
            tour_times[shortest_ship] = preview_times[shortest_ship]

            print(f"  Moved {market_to_move} from {longest_ship} to {shortest_ship}")
            print(f"    {longest_ship}: {tour_times[longest_ship]/60:.1f} min → {len(partitions[longest_ship])} markets")
            print(f"    {shortest_ship}: {tour_times[shortest_ship]/60:.1f} min → {len(partitions[shortest_ship])} markets")

        # Print final distribution
        print("\n" + "="*70)
        print("FINAL BALANCED TOUR TIMES")
        print("="*70)
        total_time = 0
        for ship in sorted(partitions.keys()):
            markets_count = len(partitions[ship])
            time_min = tour_times[ship] / 60
            total_time += tour_times[ship]
            print(f"  {ship}: {markets_count:2d} markets, {time_min:6.1f} min")

        avg_time = total_time / len([s for s in partitions if partitions[s]]) if partitions else 0
        print(f"\nAverage tour time: {avg_time/60:.1f} min")
        print("="*70 + "\n")

        # FINAL VALIDATION: Ensure no markets were dropped AND no duplicates exist
        # Step 1: Check for duplicates (markets in multiple scouts)
        all_final_markets = []
        for markets_list in partitions.values():
            all_final_markets.extend(markets_list)

        market_counts = {}
        for market in all_final_markets:
            market_counts[market] = market_counts.get(market, 0) + 1

        duplicates = {market: count for market, count in market_counts.items() if count > 1}

        if duplicates:
            duplicate_details = []
            for market, count in duplicates.items():
                ships_with_market = [ship for ship, markets in partitions.items() if market in markets]
                duplicate_details.append(f"  {market} appears {count} times in scouts: {', '.join(ships_with_market)}")

            raise RuntimeError(
                f"❌ CRITICAL: balance_tour_times() created overlapping partitions! "
                f"{len(duplicates)} markets assigned to multiple scouts:\n"
                + "\n".join(duplicate_details)
            )

        # Step 2: Check for missing/extra markets
        final_markets = set(all_final_markets)
        original_markets = set(self.markets)
        missing = original_markets - final_markets
        extra = final_markets - original_markets

        if missing or extra:
            raise RuntimeError(
                f"❌ CRITICAL: Market set changed during balance_tour_times()! "
                f"Missing: {sorted(missing) if missing else 'none'}, "
                f"Extra: {sorted(extra) if extra else 'none'}"
            )

        return partitions

    def _estimate_partition_tour_time(self, markets: List[str], ship_data: Dict) -> float:
        """
        Fast estimate of tour time using bounding box with proper game formula

        Uses actual SpaceTraders travel time formula: round((distance × mode_multiplier) / engine_speed)
        Estimates tour distance as perimeter of bounding box (better than diagonal)
        Adds overhead for dock/orbit/API cycles at each market
        """
        if not markets:
            return 0.0

        # Get all coordinates
        coords = []
        for market in markets:
            wp = self.graph['waypoints'].get(market)
            if wp:
                coords.append((wp['x'], wp['y']))

        if not coords:
            return 0.0

        # Estimate tour distance as sum of nearest-neighbor distances
        # This gives a reasonable approximation without full TSP
        if len(coords) == 1:
            estimated_distance = 0
        elif len(coords) == 2:
            # Just the round-trip distance
            estimated_distance = 2 * ((coords[1][0] - coords[0][0])**2 + (coords[1][1] - coords[0][1])**2)**0.5
        else:
            # Greedy nearest neighbor tour estimate
            visited = set()
            current = 0
            visited.add(0)
            total_dist = 0

            for _ in range(len(coords) - 1):
                nearest = None
                nearest_dist = float('inf')

                for i in range(len(coords)):
                    if i not in visited:
                        dist = ((coords[i][0] - coords[current][0])**2 + (coords[i][1] - coords[current][1])**2)**0.5
                        if dist < nearest_dist:
                            nearest_dist = dist
                            nearest = i

                if nearest is not None:
                    total_dist += nearest_dist
                    visited.add(nearest)
                    current = nearest

            # Return to start
            total_dist += ((coords[0][0] - coords[current][0])**2 + (coords[0][1] - coords[current][1])**2)**0.5
            estimated_distance = total_dist

        # Use actual game formula: round((distance × mode_multiplier) / engine_speed)
        ship_speed = ship_data.get('engine', {}).get('speed', 9)
        drift_multiplier = 26  # Empirically measured: 166 units in 476s = mult ~26
        travel_time_seconds = round((estimated_distance * drift_multiplier) / ship_speed)

        # Add overhead for each market visit:
        # - Dock: ~10 seconds
        # - Get market data API: ~2 seconds
        # - Orbit: ~10 seconds
        # Total: ~22 seconds per market
        overhead_per_market = 22
        total_overhead = len(markets) * overhead_per_market

        total_time_seconds = travel_time_seconds + total_overhead
        return total_time_seconds

    def _calculate_partition_tour_time(self, markets: List[str], ship_data: Dict) -> float:
        """
        Calculate tour time for a partition starting from its centroid using OR-Tools

        This gives a fair estimate of tour time independent of ship's current position.
        Represents the actual recurring scouting work, not one-time repositioning.
        """
        if not markets:
            return 0.0

        # Calculate partition centroid
        positions = []
        for market in markets:
            wp = self.graph['waypoints'].get(market)
            if wp:
                positions.append((wp['x'], wp['y']))

        if not positions:
            return 0.0

        centroid_x = sum(p[0] for p in positions) / len(positions)
        centroid_y = sum(p[1] for p in positions) / len(positions)

        # Find market closest to centroid to use as virtual starting position
        def dist_to_centroid(market):
            wp = self.graph['waypoints'].get(market)
            if not wp:
                return float('inf')
            return ((wp['x'] - centroid_x)**2 + (wp['y'] - centroid_y)**2)**0.5

        start_market = min(markets, key=dist_to_centroid)

        # Create virtual ship data at the centroid
        virtual_ship = ship_data.copy()
        virtual_ship['nav'] = virtual_ship['nav'].copy()
        virtual_ship['nav']['waypointSymbol'] = start_market
        virtual_ship['fuel'] = {'current': virtual_ship['fuel']['capacity'], 'capacity': virtual_ship['fuel']['capacity']}

        # Calculate tour using TourOptimizer with OR-Tools
        optimizer = TourOptimizer(self.graph, virtual_ship)
        tour = optimizer.plan_tour(
            start_market,
            markets,
            virtual_ship['fuel']['current'],
            return_to_start=True,
            algorithm='ortools',
            use_cache=False,  # Disabled: prevents stale cache with excluded markets
        )

        return tour['total_time'] if tour else float('inf')  # Return infinity if tour not possible

    def _find_boundary_market(self, from_markets: List[str], to_markets: List[str]) -> Optional[str]:
        """
        Find market in from_markets that's closest to to_markets region

        This finds "boundary" markets that can be moved without breaking locality too much.
        """
        if not from_markets:
            return None

        if not to_markets:
            # If target has no markets, just take first market from source
            return from_markets[0]

        # Get positions
        from_positions = {}
        to_positions = {}

        for market in from_markets:
            wp = self.graph['waypoints'].get(market)
            if wp:
                from_positions[market] = (wp['x'], wp['y'])

        for market in to_markets:
            wp = self.graph['waypoints'].get(market)
            if wp:
                to_positions[market] = (wp['x'], wp['y'])

        if not from_positions or not to_positions:
            return from_markets[0] if from_markets else None

        # Calculate centroid of target region
        to_centroid_x = sum(pos[0] for pos in to_positions.values()) / len(to_positions)
        to_centroid_y = sum(pos[1] for pos in to_positions.values()) / len(to_positions)

        # Find market in source closest to target centroid
        def distance_to_centroid(market: str) -> float:
            if market not in from_positions:
                return float('inf')
            x, y = from_positions[market]
            return ((x - to_centroid_x)**2 + (y - to_centroid_y)**2)**0.5

        return min(from_markets, key=distance_to_centroid)

    def _find_most_expensive_market(self, markets: List[str]) -> Optional[str]:
        """
        Find the market that contributes most to tour time (most distant outlier)

        This helps break up geographically dispersed clusters by finding the market
        that's farthest from the cluster centroid.

        SPECIAL CASE: For 2-market dispersed pairs (distance >500 units), uses
        system-wide centroid instead of pair centroid to identify which market
        is more isolated relative to ALL markets in the system.
        """
        if not markets:
            return None

        if len(markets) == 1:
            return markets[0]

        # Get positions
        positions = {}
        for market in markets:
            wp = self.graph['waypoints'].get(market)
            if wp:
                positions[market] = (wp['x'], wp['y'])

        if not positions:
            return markets[0]

        # SPECIAL CASE: For 2-market pairs, check if they're dispersed
        dispersed_result = self._dispersed_pair_handler.find_most_isolated(markets, positions)
        if dispersed_result is not None:
            return dispersed_result

        # Standard case: Calculate centroid of this partition
        centroid_x = sum(pos[0] for pos in positions.values()) / len(positions)
        centroid_y = sum(pos[1] for pos in positions.values()) / len(positions)

        # Find market farthest from centroid (most expensive to visit)
        def distance_from_centroid(market: str) -> float:
            if market not in positions:
                return 0
            x, y = positions[market]
            return ((x - centroid_x)**2 + (y - centroid_y)**2)**0.5

        return max(markets, key=distance_from_centroid)

    def optimize_subtour(self, ship: str, markets: List[str]) -> Optional[Dict]:
        """
        Optimize a subtour for a ship using OR-Tools TSP

        IMPORTANT: Uses partition centroid as starting point, NOT ship's current location.
        This ensures tours are independent of where ships happen to be stationed,
        preventing overlap when all ships start from the same waypoint.

        Args:
            ship: Ship symbol
            markets: List of markets to visit

        Returns:
            Tour dict with optimized route starting from partition centroid
        """
        if not markets:
            return None

        # Get ship data for fuel/engine specs
        ship_data = self.api.get_ship(ship)
        if not ship_data:
            print(f"❌ Failed to get ship data for {ship}")
            return None

        # Calculate partition centroid and find market closest to it
        # This gives a fair starting point independent of ship's current location
        positions = []
        for market in markets:
            wp = self.graph['waypoints'].get(market)
            if wp:
                positions.append((wp['x'], wp['y']))

        if not positions:
            print(f"❌ No waypoint positions found for markets: {markets}")
            return None

        centroid_x = sum(p[0] for p in positions) / len(positions)
        centroid_y = sum(p[1] for p in positions) / len(positions)

        # Find market closest to centroid to use as starting point
        def dist_to_centroid(market):
            wp = self.graph['waypoints'].get(market)
            if not wp:
                return float('inf')
            return ((wp['x'] - centroid_x)**2 + (wp['y'] - centroid_y)**2)**0.5

        start_location = min(markets, key=dist_to_centroid)
        print(f"   Tour starts from partition centroid: {start_location} (centroid: {centroid_x:.0f}, {centroid_y:.0f})")

        # Initialize optimizer and use OR-Tools
        optimizer = TourOptimizer(self.graph, ship_data)
        tour = optimizer.plan_tour(
            start_location,
            markets,
            ship_data['fuel']['current'],
            return_to_start=True,
            algorithm='ortools',
            use_cache=False,  # Disabled: prevents stale cache with excluded markets
        )

        return tour

    def start_scout_daemon(self, ship: str, markets: List[str]) -> Optional[str]:
        """
        Start continuous scout daemon for a ship

        Args:
            ship: Ship symbol
            markets: Markets to scout (subtour)

        Returns:
            Daemon ID if successful
        """
        # CHECK: Is ship already assigned to another daemon?
        if not self.assignment_manager.is_available(ship):
            existing = self.assignment_manager.get_assignment(ship)
            old_daemon = existing.get('daemon_id')

            print(f"⚠️  Ship {ship} already assigned to daemon {old_daemon}")
            print(f"   Stopping old daemon before starting new one...")

            # Stop old daemon
            if not self.daemon_manager.stop(old_daemon, timeout=15):
                print(f"❌ Failed to stop old daemon {old_daemon}, aborting")
                return None

            # Release ship assignment
            self.assignment_manager.release(ship, reason="redeployment")

        # Generate unique daemon ID with timestamp to avoid collisions
        timestamp = int(time.time())
        daemon_id = f"scout-{ship.split('-')[-1]}-{timestamp}"

        # Build command for continuous scouting
        command = [
            "python3", "-m", "spacetraders_bot.cli",
            "scout-markets",
            "--player-id", str(self.player_id),
            "--ship", ship,
            "--system", self.system,
            "--return-to-start",
            "--continuous",
            "--markets-list", ','.join(markets)  # Pass the specific markets assigned to this ship
        ]

        # Start daemon
        success = self.daemon_manager.start(daemon_id, command)

        if success:
            # REGISTER: Assign ship to this daemon
            assigned = self.assignment_manager.assign(
                ship=ship,
                operator="scout_coordinator",
                daemon_id=daemon_id,
                operation="scout-markets",
                metadata={'system': self.system, 'markets': markets}
            )

            if not assigned:
                print(f"⚠️  Warning: Daemon started but assignment failed for {ship}")

            print(f"✅ Started scout daemon: {daemon_id} for {ship}")
            print(f"   Markets: {len(markets)} - {', '.join(markets)}")
            return daemon_id
        else:
            print(f"❌ Failed to start daemon for {ship}")
            return None

    def partition_and_start(self):
        """Partition markets and start all scout daemons"""
        print(f"\n🔄 Partitioning {len(self.markets)} markets for {len(self.ships)} ship(s)...")
        print(f"   Input markets: {', '.join(sorted(self.markets))}")

        # Initial geographic partition
        partitions = self.partition_markets_geographic()

        # DEBUG: Count markets after geographic partitioning
        geo_count = sum(len(m) for m in partitions.values())
        print(f"\n📊 After geographic partitioning: {geo_count} markets")
        for ship, ship_markets in sorted(partitions.items()):
            print(f"   {ship}: {len(ship_markets)} markets")

        # Calculate minimum markets per scout for data freshness
        # For market intelligence, every ship should be utilized
        min_markets_per_scout = max(1, len(self.markets) // len(self.ships))
        print(f"   Enforcing minimum {min_markets_per_scout} markets/scout for data freshness")

        # Balance tour times using TSP for accurate dispersed pair detection
        # This takes longer during startup but ensures proper load balancing
        partitions = self.balance_tour_times(partitions, use_tsp=True, min_markets=min_markets_per_scout)

        # CRITICAL VALIDATION: Verify no markets were dropped AND no duplicates exist
        # Step 1: Check for duplicate assignments (markets in multiple scouts)
        all_assigned_markets = []
        for ship, markets_list in partitions.items():
            all_assigned_markets.extend(markets_list)

        # Count occurrences of each market
        market_counts = {}
        for market in all_assigned_markets:
            market_counts[market] = market_counts.get(market, 0) + 1

        # Find duplicates (markets assigned to multiple scouts)
        duplicates = {market: count for market, count in market_counts.items() if count > 1}

        if duplicates:
            duplicate_details = []
            for market, count in duplicates.items():
                ships_with_market = [ship for ship, markets in partitions.items() if market in markets]
                duplicate_details.append(f"  {market} appears {count} times in scouts: {', '.join(ships_with_market)}")

            raise RuntimeError(
                f"❌ CRITICAL: {len(duplicates)} markets assigned to multiple scouts (overlapping partitions)!\n"
                + "\n".join(duplicate_details) + "\n"
                f"Partitions must be DISJOINT (each market exactly once)."
            )

        # Step 2: Check for missing markets
        assigned_set = set(all_assigned_markets)
        expected_set = set(self.markets)

        missing = expected_set - assigned_set
        if missing:
            raise RuntimeError(
                f"❌ CRITICAL: {len(missing)} markets dropped during partitioning! "
                f"Expected {len(self.markets)}, got {len(assigned_set)}. "
                f"Missing: {sorted(missing)}"
            )

        # Step 3: Check for unexpected markets
        extra = assigned_set - expected_set
        if extra:
            raise RuntimeError(
                f"❌ CRITICAL: {len(extra)} unexpected markets appeared during partitioning! "
                f"Extra: {sorted(extra)}"
            )

        print(f"✅ Partition validation passed: All {len(self.markets)} markets assigned exactly once (disjoint partitions)")

        # Optimize and start each subtour
        self.assignments = {}

        for ship, ship_markets in partitions.items():
            if not ship_markets:
                print(f"⚠️  No markets assigned to {ship}, skipping")
                continue

            print(f"\n📍 {ship}: {len(ship_markets)} markets")
            print(f"   {', '.join(ship_markets)}")

            # Optimize subtour
            tour = self.optimize_subtour(ship, ship_markets)

            if not tour:
                print(f"❌ Failed to optimize subtour for {ship}")
                continue

            # Start daemon
            daemon_id = self.start_scout_daemon(ship, ship_markets)

            if daemon_id:
                self.assignments[ship] = SubtourAssignment(
                    ship=ship,
                    markets=ship_markets,
                    tour_time_seconds=tour['total_time'],
                    daemon_id=daemon_id
                )

                tour_time_min = tour['total_time'] / 60
                print(f"   Estimated tour time: {tour_time_min:.1f} minutes")

    def monitor_and_restart(self):
        """Monitor scout daemons and restart if they stop (continuous mode)"""
        print(f"\n🔄 Monitoring scout daemons (continuous mode)...")
        print(f"   Check interval: 30 seconds")
        print(f"   Config file: {self.config_file}")
        print(f"   Press Ctrl+C to stop\n")

        check_interval = 30  # Check every 30 seconds

        while self.running:
            self._monitor_cycle(check_interval)

        print("\n🛑 Monitoring stopped")

    def _monitor_cycle(self, check_interval: int) -> None:
        """Perform a single monitoring cycle."""
        if self._check_reconfigure_signal():
            self._handle_reconfiguration()
            return

        for health in self._collect_daemon_health():
            if not health.is_running:
                self._restart_daemon_for(health.ship, health.daemon_id)

        time.sleep(check_interval)

    def _collect_daemon_health(self) -> List[DaemonHealth]:
        """Gather the running status for all tracked scout daemons."""
        health: List[DaemonHealth] = []
        for ship, assignment in list(self.assignments.items()):
            daemon_id = assignment.daemon_id
            is_running = self.daemon_manager.is_running(daemon_id)
            health.append(DaemonHealth(ship=ship, daemon_id=daemon_id, is_running=is_running))
        return health

    def _restart_daemon_for(self, ship: str, daemon_id: str) -> None:
        """Attempt to restart the daemon associated with the given ship."""
        print(f"⚠️  Daemon {daemon_id} stopped, restarting...")

        assignment = self.assignments.get(ship)
        if not assignment:
            print(f"⚠️  No assignment found for ship {ship}; cannot restart daemon")
            return

        new_daemon_id = self.start_scout_daemon(ship, assignment.markets)
        if new_daemon_id:
            assignment.daemon_id = new_daemon_id
        else:
            print(f"❌ Failed to restart {daemon_id}")

    def _check_reconfigure_signal(self) -> bool:
        """Check if reconfiguration was requested"""
        config_path = Path(self.config_file)

        if not config_path.exists():
            return False

        try:
            with open(config_path, 'r') as f:
                config = json.load(f)

            return config.get('reconfigure', False)
        except:
            return False

    def _handle_reconfiguration(self):
        """Handle graceful reconfiguration"""
        print("\n🔄 Reconfiguration requested...")

        # Load new configuration
        with open(self.config_file, 'r') as f:
            config = json.load(f)

        new_ships = set(config.get('ships', []))

        if new_ships == self.ships:
            print("⚠️  No ship changes detected")
            # Clear reconfigure flag
            config['reconfigure'] = False
            with open(self.config_file, 'w') as f:
                json.dump(config, f, indent=2)
            return

        added = new_ships - self.ships
        removed = self.ships - new_ships

        print(f"   Added ships: {added if added else 'None'}")
        print(f"   Removed ships: {removed if removed else 'None'}")

        # Wait for current tours to complete
        print("⏳ Waiting for current tours to complete...")
        self._wait_for_tours_complete()

        # Stop removed daemons
        for ship in removed:
            if ship in self.assignments:
                daemon_id = self.assignments[ship].daemon_id
                print(f"🛑 Stopping daemon {daemon_id} for removed ship {ship}")
                self.daemon_manager.stop(daemon_id)
                del self.assignments[ship]

        # Update ship pool
        self.ships = new_ships
        self._invalidate_partitioner()

        # Repartition and restart
        print(f"\n🔄 Repartitioning {len(self.markets)} markets for {len(self.ships)} ship(s)...")
        self.partition_and_start()

        # Clear reconfigure flag
        config['reconfigure'] = False
        with open(self.config_file, 'w') as f:
            json.dump(config, f, indent=2)

        print("✅ Reconfiguration complete\n")

    def _wait_for_tours_complete(self, timeout: int = 300):
        """Wait for current tours to complete (max timeout)"""
        start_time = time.time()

        while time.time() - start_time < timeout:
            all_complete = True

            for assignment in self.assignments.values():
                if self.daemon_manager.is_running(assignment.daemon_id):
                    all_complete = False
                    break

            if all_complete:
                print("✅ All tours complete")
                return

            time.sleep(5)

        print(f"⚠️  Timeout waiting for tours to complete ({timeout}s)")

    def stop_all(self):
        """Stop all scout daemons and release ship assignments"""
        print("\n🛑 Stopping all scout daemons...")

        for ship, assignment in self.assignments.items():
            daemon_id = assignment.daemon_id
            print(f"   Stopping {daemon_id}...")
            self.daemon_manager.stop(daemon_id)

            # Release ship assignment
            self.assignment_manager.release(ship, reason="coordinator_shutdown")

        print("✅ All scouts stopped and ships released")

    def save_config(self):
        """Save current configuration"""
        config = {
            'system': self.system,
            'ships': sorted(list(self.ships)),
            'reconfigure': False,
            'last_updated': datetime.now(timezone.utc).isoformat()
        }

        config_path = Path(self.config_file)
        config_path.parent.mkdir(parents=True, exist_ok=True)

        with open(config_path, 'w') as f:
            json.dump(config, f, indent=2)

        print(f"💾 Configuration saved to {config_path}")
