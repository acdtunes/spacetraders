"""Utilities for partitioning markets amongst scout ships."""

from __future__ import annotations

import logging
import math
import random
from dataclasses import dataclass
from typing import Dict, Iterable, List, Sequence, Tuple

from .ortools_router import ORToolsFleetPartitioner
from .routing_config import RoutingConfig

logger = logging.getLogger(__name__)


@dataclass
class PartitionResult:
    """Result of a market partitioning strategy."""

    partitions: Dict[str, List[str]]
    message: str | None = None


class MarketPartitioner:
    """Encapsulates the data shared between market partitioning strategies."""

    def __init__(
        self,
        graph: Dict,
        markets: Sequence[str],
        ships: Iterable[str],
        ship_data: Dict[str, Dict] | None = None,
        *,
        rng: random.Random | None = None,
    ) -> None:
        self.graph = graph or {}
        self.markets = list(markets)
        self.ships = list(ships)
        self.ship_data = ship_data or {}
        self._rng = rng or random.Random(42)
        self._market_coords = self._extract_market_coords()

    def partition(self, strategy: str) -> PartitionResult:
        """Return market partitions for the requested strategy."""

        strategy = strategy.lower()
        strategies = {
            "greedy": GreedyPartitionStrategy(),
            "kmeans": KMeansPartitionStrategy(self._rng),
            "geographic": GeographicPartitionStrategy(),
            "ortools": ORToolsPartitionStrategy(),
        }

        if strategy not in strategies:
            raise ValueError(f"Unknown partitioning strategy '{strategy}'")

        return strategies[strategy].partition(self)

    # --- Shared helpers -------------------------------------------------

    def available_ship_ids(self) -> List[str]:
        return list(self.ships)

    def market_coordinates(self) -> Dict[str, Tuple[int, int]]:
        return dict(self._market_coords)

    def create_empty_partitions(self) -> Dict[str, List[str]]:
        return {ship: [] for ship in self.ships}

    def estimate_tour_seconds(self, ship_markets: Sequence[str]) -> int:
        coords = [self._market_coords[m] for m in ship_markets if m in self._market_coords]

        if len(coords) <= 1:
            return 0

        if len(coords) == 2:
            total_distance = 2 * self._distance(coords[0], coords[1])
        else:
            total_distance = 0.0
            for idx in range(len(coords) - 1):
                total_distance += self._distance(coords[idx], coords[idx + 1])
            total_distance += self._distance(coords[-1], coords[0])

        # Mimic historical estimate used by the coordinator
        return round((total_distance * 26) / 9) + len(coords) * 22

    def _extract_market_coords(self) -> Dict[str, Tuple[int, int]]:
        waypoints = self.graph.get("waypoints", {})
        return {
            market: (wp["x"], wp["y"])
            for market in self.markets
            if (wp := waypoints.get(market))
        }

    @staticmethod
    def _distance(lhs: Tuple[int, int], rhs: Tuple[int, int]) -> float:
        return math.hypot(rhs[0] - lhs[0], rhs[1] - lhs[1])


class GreedyPartitionStrategy:
    """Assign each market to the ship with the currently shortest tour time."""

    def partition(self, partitioner: MarketPartitioner) -> PartitionResult:
        ships = partitioner.available_ship_ids()
        if not ships:
            return PartitionResult({}, "No ships available for greedy assignment")

        coords = partitioner.market_coordinates()
        if not coords:
            return PartitionResult(partitioner.create_empty_partitions(), "No coordinates available for greedy assignment")

        partitions = partitioner.create_empty_partitions()
        tour_times = {ship: 0.0 for ship in ships}

        sorted_markets = sorted(coords.keys(), key=lambda market: coords[market][0])

        for market in sorted_markets:
            target_ship = min(tour_times, key=tour_times.get)
            partitions[target_ship].append(market)
            tour_times[target_ship] = partitioner.estimate_tour_seconds(partitions[target_ship])

        return PartitionResult(partitions, "✅ Greedy assignment complete")


class KMeansPartitionStrategy:
    """Cluster markets spatially using a deterministic random seed."""

    def __init__(self, rng: random.Random | None = None) -> None:
        self._rng = rng or random.Random(42)

    def partition(self, partitioner: MarketPartitioner) -> PartitionResult:
        ships = partitioner.available_ship_ids()
        if not ships:
            return PartitionResult({}, "No ships available for K-means clustering")

        coords = partitioner.market_coordinates()
        markets = list(coords.keys())
        if not markets:
            return PartitionResult(partitioner.create_empty_partitions(), "No coordinates available for K-means clustering")

        k = len(ships)

        centroids = self._initial_centroids(coords, markets, k)

        max_iterations = 50
        iterations = 0

        for iterations in range(1, max_iterations + 1):
            clusters = self._assign_to_centroids(coords, centroids)
            new_centroids = self._recalculate_centroids(coords, clusters, centroids)

            if new_centroids == centroids:
                break
            centroids = new_centroids

        partitions = self._clusters_to_partitions(ships, markets, clusters)

        return PartitionResult(partitions, f"✅ K-means clustering converged in {iterations} iterations")

    def _initial_centroids(
        self,
        coords: Dict[str, Tuple[int, int]],
        markets: List[str],
        k: int,
    ) -> List[Tuple[int, int]]:
        if k == 0:
            return []

        unique_coords = [coords[market] for market in markets]
        sample_size = min(len(unique_coords), k)

        if sample_size == 0:
            return [(0, 0)] * k

        indices = list(range(len(unique_coords)))
        self._rng.seed(42)
        selected = self._rng.sample(indices, sample_size)
        centroids = [unique_coords[idx] for idx in selected]

        while len(centroids) < k:
            centroids.append(centroids[-1])

        return centroids

    @staticmethod
    def _assign_to_centroids(
        coords: Dict[str, Tuple[int, int]],
        centroids: List[Tuple[int, int]],
    ) -> List[List[int]]:
        if not centroids:
            return []

        clusters = [[] for _ in centroids]
        centroid_distances = [0.0] * len(centroids)

        markets = list(coords.items())
        for idx, (_, coord) in enumerate(markets):
            for centroid_idx, centroid in enumerate(centroids):
                centroid_distances[centroid_idx] = math.hypot(coord[0] - centroid[0], coord[1] - centroid[1])

            nearest = min(range(len(centroids)), key=lambda index: centroid_distances[index])
            clusters[nearest].append(idx)

        return clusters

    @staticmethod
    def _recalculate_centroids(
        coords: Dict[str, Tuple[int, int]],
        clusters: List[List[int]],
        centroids: List[Tuple[int, int]],
    ) -> List[Tuple[int, int]]:
        markets = list(coords.values())
        new_centroids: List[Tuple[int, int]] = []

        for cluster_index, cluster in enumerate(clusters):
            if not cluster:
                new_centroids.append(centroids[cluster_index])
                continue

            avg_x = sum(markets[idx][0] for idx in cluster) / len(cluster)
            avg_y = sum(markets[idx][1] for idx in cluster) / len(cluster)
            new_centroids.append((avg_x, avg_y))

        return new_centroids

    @staticmethod
    def _clusters_to_partitions(
        ships: Sequence[str],
        markets: List[str],
        clusters: List[List[int]],
    ) -> Dict[str, List[str]]:
        partitions = {ship: [] for ship in ships}

        for ship_idx, ship in enumerate(ships):
            if ship_idx < len(clusters):
                partitions[ship] = [markets[i] for i in clusters[ship_idx]]

        return partitions


class GeographicPartitionStrategy:
    """Slice markets geographically to create contiguous subtours."""

    def partition(self, partitioner: MarketPartitioner) -> PartitionResult:
        ships = partitioner.available_ship_ids()
        if not ships:
            return PartitionResult({}, "No ships available for geographic partitioning")

        markets = partitioner.markets
        if not markets:
            return PartitionResult(partitioner.create_empty_partitions(), None)

        if len(ships) == 1:
            return PartitionResult({ships[0]: list(markets)}, None)

        positions = partitioner.market_coordinates()
        if not positions:
            return PartitionResult(partitioner.create_empty_partitions(), None)

        min_x = min(pos[0] for pos in positions.values())
        max_x = max(pos[0] for pos in positions.values())
        min_y = min(pos[1] for pos in positions.values())
        max_y = max(pos[1] for pos in positions.values())

        width = max_x - min_x
        height = max_y - min_y

        ship_list = list(ships)

        if width == 0 and height == 0:
            partitions = self._distribute_evenly(list(positions.keys()), ship_list)
        elif width > height:
            partitions = self._partition_by_axis(positions, ship_list, axis="x", min_value=min_x, max_value=max_x)
        else:
            partitions = self._partition_by_axis(positions, ship_list, axis="y", min_value=min_y, max_value=max_y)

        return PartitionResult(partitions, None)

    @staticmethod
    def _distribute_evenly(markets: List[str], ships: Sequence[str]) -> Dict[str, List[str]]:
        partitions = {ship: [] for ship in ships}
        ship_count = len(ships)

        if ship_count == 0:
            return partitions

        for index, market in enumerate(markets):
            partitions[ships[index % ship_count]].append(market)

        return partitions

    def _partition_by_axis(
        self,
        positions: Dict[str, Tuple[int, int]],
        ships: Sequence[str],
        *,
        axis: str,
        min_value: int,
        max_value: int,
    ) -> Dict[str, List[str]]:
        span = max_value - min_value
        if span == 0:
            return self._distribute_evenly(list(positions.keys()), ships)

        partitions = {ship: [] for ship in ships}
        slice_size = span / len(ships)

        for market, (x, y) in positions.items():
            value = x if axis == "x" else y
            slice_idx = min(int((value - min_value) / slice_size), len(ships) - 1)
            partitions[ships[slice_idx]].append(market)

        return partitions


class ORToolsPartitionStrategy:
    """Use OR-Tools VRP to partition markets across ships."""

    def __init__(self) -> None:
        self._config = RoutingConfig()

    def partition(self, partitioner: MarketPartitioner) -> PartitionResult:
        if not partitioner.ships or not partitioner.markets:
            return PartitionResult(partitioner.create_empty_partitions(), "No ships or markets for OR-Tools partitioning")

        if not partitioner.ship_data:
            return PartitionResult(partitioner.create_empty_partitions(), "Missing ship data for OR-Tools partitioning")

        fleet = ORToolsFleetPartitioner(partitioner.graph, self._config)
        try:
            assignments = fleet.partition_and_optimize(
                markets=partitioner.markets,
                ships=partitioner.available_ship_ids(),
                ship_data=partitioner.ship_data,
            )
            return PartitionResult(assignments, "✅ OR-Tools partitioning complete")
        except Exception as exc:
            logger.exception("OR-Tools partitioning failed")
            return PartitionResult(
                partitioner.create_empty_partitions(),
                f"❌ OR-Tools partitioning failed: {exc}",
            )
