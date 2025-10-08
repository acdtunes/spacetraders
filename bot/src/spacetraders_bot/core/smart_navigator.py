#!/usr/bin/env python3
from __future__ import annotations

"""
Smart Navigator - Integrates routing engine into bot operations

Provides intelligent navigation that:
- Uses A* pathfinding with fuel constraints
- Automatically chooses optimal flight modes
- Inserts refuel stops when needed
- Validates routes before executing
"""

import logging
from pathlib import Path
from typing import Dict, Optional, Tuple

from ..helpers import paths
from .database import Database
from .routing import RouteOptimizer
from .system_graph_provider import SystemGraphProvider

logger = logging.getLogger(__name__)


class SmartNavigator:
    """
    Intelligent navigation system that integrates routing engine
    with ship operations
    """

    def __init__(
        self,
        api_client,
        system: str,
        cache_dir: str = 'graphs',
        graph: Optional[Dict] = None,
        db_path: Optional[str | Path] = None,
    ):
        """
        Initialize smart navigator

        Args:
            api_client: APIClient instance
            system: System symbol (e.g., X1-HU87)
            cache_dir: Directory for cached graphs (DEPRECATED - use database)
            graph: Pre-built graph (optional, for testing)
            db_path: Path to SQLite database (default: data/spacetraders.db)
        """
        self.api = api_client
        self.system = system
        self.cache_dir = Path(cache_dir)  # Keep for backward compatibility
        self.graph = graph
        resolved_db_path = Path(db_path) if db_path else paths.sqlite_path()
        self.db = Database(resolved_db_path)
        self.graph_provider = SystemGraphProvider(
            api_client,
            db=self.db,
            cache_dir=self.cache_dir,
        )

        # Load or build graph if not provided
        if self.graph is None:
            self._ensure_graph()

    def _ensure_graph(self):
        """Load graph from database cache or build if needed"""
        result = self.graph_provider.get_graph(self.system)
        self.graph = result.graph
        if result.message:
            logger.info(result.message)

    def plan_route(self, ship_data: Dict, destination: str,
                   prefer_cruise: bool = True) -> Optional[Dict]:
        """
        Plan optimal route from ship's current location to destination

        Args:
            ship_data: Ship data from API
            destination: Destination waypoint symbol
            prefer_cruise: Prefer CRUISE mode when fuel allows

        Returns:
            Route plan dict or None if no route found
        """
        current_location = ship_data['nav']['waypointSymbol']
        current_fuel = ship_data['fuel']['current']

        # Use RouteOptimizer
        optimizer = RouteOptimizer(self.graph, ship_data)
        route = optimizer.find_optimal_route(
            current_location,
            destination,
            current_fuel,
            prefer_cruise=prefer_cruise
        )

        return route

    def validate_route(self, ship_data: Dict, destination: str) -> Tuple[bool, str]:
        """
        Validate if route is possible with current fuel

        Args:
            ship_data: Ship data from API
            destination: Destination waypoint symbol

        Returns:
            (is_valid, reason) tuple
        """
        # Always prefer CRUISE - only fall back to DRIFT if insufficient fuel
        route = self.plan_route(ship_data, destination, prefer_cruise=True)

        if not route:
            return False, "No route found (insufficient fuel even with DRIFT)"

        # Check if route requires refueling
        needs_refuel = any(step['action'] == 'refuel' for step in route['steps'])

        if needs_refuel:
            return True, f"Route requires refuel stop(s)"

        # Validate that we have reasonable fuel margin
        # If the route uses >90% of current fuel, it's too risky without refuel stops
        from spacetraders_bot.core.routing import FuelCalculator
        current_location = ship_data['nav']['waypointSymbol']
        current_wp = self.graph['waypoints'].get(current_location)
        dest_wp = self.graph['waypoints'].get(destination)

        if current_wp and dest_wp:
            import math
            distance = math.sqrt(
                (dest_wp['x'] - current_wp['x']) ** 2 +
                (dest_wp['y'] - current_wp['y']) ** 2
            )

            # Check if single jump is too long without refueling
            drift_fuel = FuelCalculator.fuel_cost(distance, 'DRIFT')
            current_fuel = ship_data['fuel']['current']

            # If distance is very far (>500 units) and no refuel stops, require more fuel buffer
            if distance > 500 and not needs_refuel:
                # Require at least 2x the one-way fuel for safety
                if current_fuel < drift_fuel * 2:
                    return False, f"Insufficient fuel for long-distance travel (need ~{drift_fuel * 2:.0f} for safety, have {current_fuel})"

        return True, "Route OK"

    def get_fuel_estimate(self, ship_data: Dict, destination: str) -> Optional[Dict]:
        """
        Estimate fuel needed for route

        Args:
            ship_data: Ship data from API
            destination: Destination waypoint symbol

        Returns:
            Dict with fuel estimates or None
        """
        route = self.plan_route(ship_data, destination)

        if not route:
            return None

        total_fuel_cost = sum(
            step['fuel_cost'] for step in route['steps']
            if step['action'] == 'navigate'
        )

        refuel_stops = [
            step for step in route['steps']
            if step['action'] == 'refuel'
        ]

        return {
            'total_fuel_cost': total_fuel_cost,
            'final_fuel': route['final_fuel'],
            'refuel_stops': len(refuel_stops),
            'total_time': route['total_time'],
            'feasible': True
        }

    # State machine transition table
    STATE_TRANSITIONS = {
        ('DOCKED', 'IN_ORBIT'): 'orbit',
        ('DOCKED', 'DOCKED'): 'noop',
        ('IN_ORBIT', 'DOCKED'): 'dock',
        ('IN_ORBIT', 'IN_ORBIT'): 'noop',
        ('IN_TRANSIT', 'IN_ORBIT'): 'wait',
        ('IN_TRANSIT', 'DOCKED'): 'wait_then_dock',
        ('IN_TRANSIT', 'IN_TRANSIT'): 'noop',
    }

    def _handle_orbit(self, ship_controller, ship_data: Dict) -> bool:
        """Transition handler: orbit ship"""
        logger.info("State transition: DOCKED → IN_ORBIT")
        return ship_controller.orbit()

    def _handle_dock(self, ship_controller, ship_data: Dict) -> bool:
        """Transition handler: dock ship"""
        logger.info("State transition: IN_ORBIT → DOCKED")
        return ship_controller.dock()

    def _handle_wait(self, ship_controller, ship_data: Dict) -> bool:
        """Transition handler: wait for arrival"""
        arrival = ship_data['nav']['route']['arrival']
        destination = ship_data['nav']['route']['destination']['symbol']
        from spacetraders_bot.core.utils import calculate_arrival_wait_time
        wait_time = calculate_arrival_wait_time(arrival)
        logger.info(f"State transition: IN_TRANSIT → IN_ORBIT (waiting {wait_time}s for arrival at {destination})")
        ship_controller._wait_for_arrival(wait_time + 3)
        return True

    def _handle_wait_then_dock(self, ship_controller, ship_data: Dict) -> bool:
        """Transition handler: wait for arrival then dock"""
        arrival = ship_data['nav']['route']['arrival']
        destination = ship_data['nav']['route']['destination']['symbol']
        from spacetraders_bot.core.utils import calculate_arrival_wait_time
        wait_time = calculate_arrival_wait_time(arrival)
        logger.info(f"State transition: IN_TRANSIT → DOCKED (waiting {wait_time}s for arrival at {destination})")
        ship_controller._wait_for_arrival(wait_time + 3)
        return ship_controller.dock()

    def _handle_noop(self, ship_controller, ship_data: Dict) -> bool:
        """Transition handler: already in correct state"""
        return True

    def _ensure_valid_state(self, ship_controller, required_state: str) -> bool:
        """
        Ensure ship is in required state, transitioning if necessary

        Uses a transition table and handler pattern for clean state management.

        Args:
            ship_controller: ShipController instance
            required_state: One of 'DOCKED', 'IN_ORBIT', or 'IN_TRANSIT'

        Returns:
            True if ship is in required state or successfully transitioned, False otherwise
        """
        ship_data = ship_controller.get_status()
        if not ship_data:
            logger.error("Failed to get ship status for state validation")
            return False

        current_state = ship_data['nav']['status']

        # Lookup transition in table
        transition_key = (current_state, required_state)
        if transition_key not in self.STATE_TRANSITIONS:
            logger.error(f"Invalid state transition: {current_state} → {required_state}")
            return False

        # Get handler for this transition
        handler_name = self.STATE_TRANSITIONS[transition_key]
        handler = getattr(self, f'_handle_{handler_name}')

        # Execute transition
        return handler(ship_controller, ship_data)

    def _validate_ship_health(self, ship_data: Dict) -> Tuple[bool, str]:
        """
        Validate ship is in good condition to navigate

        Returns:
            (is_valid, reason) tuple
        """
        # Check frame integrity
        frame = ship_data.get('frame', {})
        integrity = frame.get('integrity', 1.0)

        # Convert integrity from decimal (0.0-1.0) to percentage (0-100)
        if integrity <= 1.0:
            integrity_pct = integrity * 100
        else:
            integrity_pct = integrity

        if integrity_pct < 50:
            return False, f"Critical damage: {integrity_pct:.0f}% integrity (requires repair)"
        elif integrity_pct < 75:
            logger.warning(f"Ship damage: {integrity_pct:.0f}% integrity (consider repair)")

        # Check if ship has fuel capacity (allow probe ships with 0 fuel capacity - they don't consume fuel)
        fuel = ship_data.get('fuel', {})
        registration = ship_data.get('registration', {})
        ship_role = registration.get('role', '').upper()  # Normalize to uppercase

        logger.debug(f"Ship registration role: '{ship_role}', fuel capacity: {fuel.get('capacity', 0)}")

        # Probe/satellite ships have 0 fuel capacity by design (no fuel consumption)
        if fuel.get('capacity', 0) == 0 and ship_role not in ['SATELLITE', 'PROBE']:
            return False, f"Ship has no fuel capacity (cannot navigate) - role: {ship_role}"

        # Check cooldown
        cooldown = ship_data.get('cooldown', {})
        remaining_cooldown = cooldown.get('remainingSeconds', 0)

        if remaining_cooldown > 0:
            logger.warning(f"Ship has {remaining_cooldown}s cooldown remaining")

        return True, "Ship health OK"

    def _handle_in_transit_state(
        self,
        ship_controller,
        ship_data: Dict,
        destination: str,
    ) -> Tuple[Optional[Dict], bool]:
        """Wait for a ship already in transit and refresh its status."""
        route_info = ship_data['nav']['route']
        in_transit_dest = route_info['destination']['symbol']

        from spacetraders_bot.core.utils import calculate_arrival_wait_time

        wait_time = calculate_arrival_wait_time(route_info['arrival'])

        if in_transit_dest == destination:
            logger.info(f"Already in transit to {destination}")
        else:
            logger.info(
                "Ship in transit to %s, waiting for arrival before planning route to %s",
                in_transit_dest,
                destination,
            )

        ship_controller._wait_for_arrival(wait_time + 3)
        updated_data = ship_controller.get_status()

        if not updated_data:
            logger.error("Failed to get ship status after arrival")
            return None, False

        if in_transit_dest == destination:
            logger.info(f"✅ Arrived at {destination}")
            return updated_data, True

        return updated_data, False

    def execute_route(self, ship_controller, destination: str,
                     prefer_cruise: bool = True,
                     operation_controller=None) -> bool:
        """
        Plan and execute optimal route with robust state machine

        State Machine Flow:
        1. Validate ship health and current state
        2. Handle IN_TRANSIT state (wait for arrival)
        3. Ensure ship is IN_ORBIT before navigation
        4. Execute each navigation step
        5. Handle refuel stops (transition to DOCKED)
        6. Verify final state

        Args:
            ship_controller: ShipController instance
            destination: Destination waypoint symbol
            prefer_cruise: Prefer CRUISE mode when possible (True = fast, False = economical)

        Returns:
            True if navigation successful and arrived at destination, False otherwise
        """
        # Get current ship status
        ship_data = ship_controller.get_status()
        if not ship_data:
            logger.error("Failed to get ship status")
            return False

        # Validate ship health
        is_healthy, health_reason = self._validate_ship_health(ship_data)
        if not is_healthy:
            logger.error(f"Ship health check failed: {health_reason}")
            return False

        current_location = ship_data['nav']['waypointSymbol']
        current_state = ship_data['nav']['status']

        # Already at destination
        if current_location == destination:
            logger.info(f"Already at {destination}")
            return True

        # Handle IN_TRANSIT state - must wait before planning new route
        if current_state == 'IN_TRANSIT':
            ship_data, arrived = self._handle_in_transit_state(ship_controller, ship_data, destination)
            if not ship_data:
                return False
            if arrived:
                return True

            current_location = ship_data['nav']['waypointSymbol']
            current_state = ship_data['nav']['status']

        # Plan route from current location
        route = self.plan_route(ship_data, destination, prefer_cruise)
        if not route:
            logger.error(f"No route found from {current_location} to {destination}")
            return False

        from spacetraders_bot.core.routing import TimeCalculator
        logger.info(f"Executing route: {len(route['steps'])} steps, "
                   f"{TimeCalculator.format_time(route['total_time'])} total time")

        # Log complete route plan (including refuel stops)
        logger.info("📋 ROUTE PLAN:")
        for i, step in enumerate(route['steps'], 1):
            if step['action'] == 'navigate':
                logger.info(f"   {i}. Navigate {step['from']} → {step['to']} ({step['mode']}, {step['distance']:.0f}u, {step['fuel_cost']}⛽)")
            elif step['action'] == 'refuel':
                logger.info(f"   {i}. Refuel at {step['waypoint']} (+{step['fuel_added']}⛽)")

        # Execute each step with state machine
        start_step = 1

        # Resume from checkpoint if available
        if operation_controller and operation_controller.can_resume():
            checkpoint = operation_controller.get_last_checkpoint()
            if checkpoint:
                start_step = checkpoint.get('completed_step', 0) + 1
                logger.info(f"Resuming from step {start_step}/{len(route['steps'])}")

        for i, step in enumerate(route['steps'], 1):
            # Skip already completed steps
            if i < start_step:
                continue

            # Check for external control commands
            if operation_controller:
                if operation_controller.should_cancel():
                    logger.warning("Operation cancelled by external command")
                    operation_controller.cancel()
                    return False

                if operation_controller.should_pause():
                    logger.info("Operation paused by external command")
                    operation_controller.pause()
                    return False

            if step['action'] == 'navigate':
                logger.info(f"Step {i}/{len(route['steps'])}: Navigate {step['from']} → {step['to']} "
                           f"({step['mode']}, {step['distance']:.0f}u, {step['fuel_cost']}⛽)")

                # Ensure ship is IN_ORBIT before navigation
                if not self._ensure_valid_state(ship_controller, 'IN_ORBIT'):
                    logger.error(f"Failed to transition to IN_ORBIT for navigation")
                    return False

                # Navigate using ShipController (handles waiting automatically)
                success = ship_controller.navigate(
                    waypoint=step['to'],
                    flight_mode=step['mode'],
                    auto_refuel=False  # We handle refueling via planned stops
                )

                if not success:
                    logger.error(f"Navigation failed at step {i}")
                    return False

                # Verify arrival
                current_ship_data = ship_controller.get_status()
                if not current_ship_data:
                    logger.error("Failed to get ship status after navigation")
                    return False

                actual_location = current_ship_data['nav']['waypointSymbol']
                actual_state = current_ship_data['nav']['status']

                if actual_location != step['to']:
                    logger.error(f"Navigation error: expected {step['to']}, arrived at {actual_location}")
                    return False

                if actual_state != 'IN_ORBIT':
                    logger.warning(f"Unexpected state after navigation: {actual_state} (expected IN_ORBIT)")

                logger.info(f"✅ Arrived at {step['to']} (state: {actual_state})")

                # Save checkpoint
                if operation_controller:
                    operation_controller.checkpoint({
                        'completed_step': i,
                        'location': actual_location,
                        'fuel': current_ship_data['fuel']['current'],
                        'state': actual_state
                    })

            elif step['action'] == 'refuel':
                logger.info(f"Step {i}/{len(route['steps'])}: Refuel at {step['waypoint']} "
                           f"(+{step['fuel_added']}⛽)")

                # BUGFIX: Check if we're at the refuel waypoint
                # If not, navigate there first
                current_ship_data = ship_controller.get_status()
                if not current_ship_data:
                    logger.error("Failed to get ship status before refuel")
                    return False

                current_location = current_ship_data['nav']['waypointSymbol']
                refuel_waypoint = step['waypoint']

                if current_location != refuel_waypoint:
                    logger.info(f"Navigating to refuel waypoint: {current_location} → {refuel_waypoint}")

                    # Ensure ship is IN_ORBIT before navigation
                    if not self._ensure_valid_state(ship_controller, 'IN_ORBIT'):
                        logger.error(f"Failed to transition to IN_ORBIT for navigation to refuel stop")
                        return False

                    # Navigate to refuel waypoint
                    success = ship_controller.navigate(
                        waypoint=refuel_waypoint,
                        flight_mode='DRIFT',  # Use DRIFT to conserve fuel
                        auto_refuel=False
                    )

                    if not success:
                        logger.error(f"Navigation to refuel waypoint {refuel_waypoint} failed")
                        return False

                    # Verify arrival at refuel waypoint
                    current_ship_data = ship_controller.get_status()
                    if not current_ship_data:
                        logger.error("Failed to get ship status after navigation to refuel stop")
                        return False

                    actual_location = current_ship_data['nav']['waypointSymbol']
                    if actual_location != refuel_waypoint:
                        logger.error(f"Navigation error: expected {refuel_waypoint}, arrived at {actual_location}")
                        return False

                    logger.info(f"✅ Arrived at refuel stop {refuel_waypoint}")

                # Ensure ship is DOCKED for refueling
                if not self._ensure_valid_state(ship_controller, 'DOCKED'):
                    logger.error("Failed to dock for refueling")
                    return False

                # Get fuel before refueling
                status = ship_controller.get_status()
                fuel_before = status['fuel']['current'] if status else 0

                # Refuel to full
                if not ship_controller.refuel():
                    logger.error("Refuel failed")
                    return False

                # Verify refuel
                status_after = ship_controller.get_status()
                fuel_after = status_after['fuel']['current'] if status_after else 0
                logger.info(f"✅ Refueled: {fuel_before} → {fuel_after}")

                # Save checkpoint
                if operation_controller:
                    operation_controller.checkpoint({
                        'completed_step': i,
                        'location': step['waypoint'],
                        'fuel': fuel_after,
                        'state': 'DOCKED'
                    })

        # Final verification
        final_ship_data = ship_controller.get_status()
        if not final_ship_data:
            logger.warning("Could not verify final position")
            return True  # Navigation succeeded even if we can't verify

        final_location = final_ship_data['nav']['waypointSymbol']
        final_state = final_ship_data['nav']['status']
        final_fuel = final_ship_data['fuel']['current']

        if final_location != destination:
            logger.error(f"Route execution failed: ended at {final_location}, expected {destination}")
            return False

        logger.info(f"✅ Route execution complete. Arrived at {destination} (state: {final_state}, fuel: {final_fuel}⛽)")

        # PROACTIVE REFUELING: When prefer_cruise=True, refuel at destination if needed
        # This ensures next navigation can use CRUISE mode
        if prefer_cruise and final_fuel < final_ship_data['fuel']['capacity'] * 0.75:
            # Check if destination has marketplace for refueling
            dest_wp = self.graph['waypoints'].get(destination, {})
            has_marketplace = 'MARKETPLACE' in dest_wp.get('traits', [])

            if has_marketplace:
                logger.info(f"🔋 Proactive refuel: {final_fuel}/{final_ship_data['fuel']['capacity']} fuel (maintaining CRUISE capability)")

                # Dock if needed for refuel
                if final_state != 'DOCKED':
                    if not self._ensure_valid_state(ship_controller, 'DOCKED'):
                        logger.warning("⚠️  Failed to dock for proactive refuel")
                        return True  # Still return success since we arrived

                # Refuel
                if ship_controller.refuel():
                    after_refuel = ship_controller.get_status()
                    if after_refuel:
                        new_fuel = after_refuel['fuel']['current']
                        logger.info(f"✅ Refueled: {final_fuel} → {new_fuel} fuel")
                else:
                    logger.warning("⚠️  Proactive refuel failed")

        return True

    def find_nearest_with_trait(self, ship_data: Dict, trait: str,
                                max_results: int = 5) -> list:
        """
        Find nearest waypoints with specific trait

        Args:
            ship_data: Ship data from API
            trait: Trait to search for (e.g., 'MARKETPLACE')
            max_results: Maximum number of results

        Returns:
            List of waypoint dicts sorted by distance
        """
        from routing import euclidean_distance

        current_location = ship_data['nav']['waypointSymbol']
        current_wp = self.graph['waypoints'][current_location]

        results = []

        for wp_symbol, wp_data in self.graph['waypoints'].items():
            if trait in wp_data.get('traits', []):
                distance = euclidean_distance(
                    current_wp['x'], current_wp['y'],
                    wp_data['x'], wp_data['y']
                )

                results.append({
                    'symbol': wp_symbol,
                    'distance': distance,
                    'type': wp_data['type'],
                    'traits': wp_data['traits']
                })

        # Sort by distance
        results.sort(key=lambda x: x['distance'])

        return results[:max_results]
