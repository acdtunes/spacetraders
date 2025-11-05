"""Port interface for routing engine operations"""
from abc import ABC, abstractmethod
from typing import Optional, Dict, List, Any
from domain.shared.value_objects import Waypoint, FlightMode


class IRoutingEngine(ABC):
    """
    Port interface for pathfinding and route optimization.

    Provides algorithms for:
    - Optimal pathfinding with fuel constraints
    - Traveling salesman problem (TSP) for multi-waypoint tours
    - Fuel and time cost calculations
    """

    @abstractmethod
    def find_optimal_path(
        self,
        graph: Dict[str, Waypoint],
        start: str,
        goal: str,
        current_fuel: int,
        fuel_capacity: int,
        engine_speed: int,
        prefer_cruise: bool = True
    ) -> Optional[Dict[str, Any]]:
        """
        Find optimal path between two waypoints considering fuel constraints.

        Args:
            graph: Dict mapping waypoint symbols to Waypoint objects
            start: Starting waypoint symbol
            goal: Goal waypoint symbol
            current_fuel: Current fuel amount
            fuel_capacity: Maximum fuel capacity
            engine_speed: Ship's engine speed modifier
            prefer_cruise: Whether to prefer CRUISE mode when possible

        Returns:
            Dict with route details or None if no path exists:
            {
                'steps': [
                    {
                        'action': 'TRAVEL' | 'REFUEL',
                        'waypoint': waypoint_symbol,
                        'fuel_cost': int,
                        'time': int,
                        'mode': FlightMode (for TRAVEL),
                        'distance': float (for TRAVEL)
                    }
                ],
                'total_fuel_cost': int,
                'total_time': int,
                'total_distance': float
            }
        """
        pass

    @abstractmethod
    def optimize_tour(
        self,
        graph: Dict[str, Waypoint],
        waypoints: List[str],
        start: str,
        return_to_start: bool,
        fuel_capacity: int,
        engine_speed: int
    ) -> Optional[Dict[str, Any]]:
        """
        Optimize visiting order for multiple waypoints (TSP).

        Args:
            graph: Dict mapping waypoint symbols to Waypoint objects
            waypoints: List of waypoint symbols to visit
            start: Starting waypoint symbol
            return_to_start: Whether to return to start after visiting all
            fuel_capacity: Maximum fuel capacity
            engine_speed: Ship's engine speed modifier

        Returns:
            Dict with tour details or None if no valid tour exists:
            {
                'ordered_waypoints': [waypoint_symbols],
                'legs': [
                    {
                        'from': waypoint_symbol,
                        'to': waypoint_symbol,
                        'distance': float,
                        'fuel_cost': int,
                        'time': int,
                        'mode': FlightMode
                    }
                ],
                'total_distance': float,
                'total_fuel_cost': int,
                'total_time': int
            }
        """
        pass

    @abstractmethod
    def calculate_fuel_cost(self, distance: float, mode: FlightMode) -> int:
        """
        Calculate fuel cost for given distance and flight mode.

        Args:
            distance: Distance in units
            mode: Flight mode (CRUISE, DRIFT, etc.)

        Returns:
            Fuel cost in units
        """
        pass

    @abstractmethod
    def calculate_travel_time(
        self,
        distance: float,
        mode: FlightMode,
        engine_speed: int
    ) -> int:
        """
        Calculate travel time for given distance, mode, and engine speed.

        Args:
            distance: Distance in units
            mode: Flight mode (CRUISE, DRIFT, etc.)
            engine_speed: Ship's engine speed modifier

        Returns:
            Travel time in seconds
        """
        pass

    @abstractmethod
    def optimize_fleet_tour(
        self,
        graph: Dict[str, Waypoint],
        markets: List[str],
        ship_locations: Dict[str, str],  # ship_symbol -> current_waypoint
        fuel_capacity: int,
        engine_speed: int
    ) -> Optional[Dict[str, List[str]]]:
        """
        Partition markets across multiple ships (Vehicle Routing Problem).

        Solves multi-vehicle VRP to create disjoint tours where each market
        is visited by exactly ONE ship. Uses OR-Tools routing with multiple
        vehicles, start/end depots, and disjunction penalties.

        Args:
            graph: Dict mapping waypoint symbols to Waypoint objects
            markets: List of market waypoint symbols to partition
            ship_locations: Dict mapping ship_symbol to current waypoint
            fuel_capacity: Ship fuel capacity (assumes homogeneous fleet)
            engine_speed: Ship engine speed (assumes homogeneous fleet)

        Returns:
            Dict mapping ship_symbol -> List[assigned_market_waypoints]
            Each market appears in exactly ONE ship's list.
            Returns None if partitioning fails.
        """
        pass
