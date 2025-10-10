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
from .routing_config import RoutingConfig
from .ortools_router import ORToolsRouter
from .routing_pause import is_paused as routing_paused, get_pause_details
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
        self.routing_config = RoutingConfig()

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

        if routing_paused():
            details = get_pause_details() or {}
            logger.warning("Routing paused: %s", details.get("reason", "Validation failure"))
            return None

        prefer_cruise = True  # Cruise preference enforced globally

        optimizer = ORToolsRouter(self.graph, ship_data, self.routing_config)
        route = optimizer.find_optimal_route(
            current_location,
            destination,
            current_fuel,
            prefer_cruise=True
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
        if routing_paused():
            details = get_pause_details() or {}
            return False, f"Routing paused: {details.get('reason', 'Validation failure')}"

        # Always prefer CRUISE - only fall back to DRIFT if insufficient fuel
        route = self.plan_route(ship_data, destination, prefer_cruise=True)

        if not route:
            # Calculate fuel feasibility to provide accurate error message
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
                drift_fuel = FuelCalculator.fuel_cost(distance, 'DRIFT')
                current_fuel = ship_data['fuel']['current']

                if current_fuel >= drift_fuel:
                    # Fuel is adequate - route planning failed for other reasons
                    return False, f"No route found (complex pathfinding - distance: {distance:.0f} units, may require refuel stops or intermediate waypoints)"
                else:
                    # Fuel is actually insufficient
                    return False, f"Insufficient fuel (need {drift_fuel} for DRIFT, have {current_fuel})"

            return False, "No route found"

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
        ship_data = self._get_status_or_log(
            ship_controller,
            "Failed to get ship status for state validation",
        )
        if not ship_data:
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
        updated_data = self._get_status_or_log(
            ship_controller,
            "Failed to get ship status after arrival",
        )

        if not updated_data:
            return None, False

        if in_transit_dest == destination:
            logger.info(f"✅ Arrived at {destination}")
            return updated_data, True

        return updated_data, False

    def _get_status_or_log(self, ship_controller, error_message: str) -> Optional[Dict]:
        """Fetch ship status, logging an error when unavailable."""
        status = ship_controller.get_status()
        if not status:
            logger.error(error_message)
        return status

    def _should_continue_operation(self, operation_controller) -> bool:
        """Handle external control commands for long-running operations."""
        if not operation_controller:
            return True

        if operation_controller.should_cancel():
            logger.warning("Operation cancelled by external command")
            operation_controller.cancel()
            return False

        if operation_controller.should_pause():
            logger.info("Operation paused by external command")
            operation_controller.pause()
            return False

        return True

    def _execute_navigate_step(
        self,
        ship_controller,
        step: Dict,
        step_index: int,
        total_steps: int,
        operation_controller,
    ) -> bool:
        logger.info(
            "Step %s/%s: Navigate %s → %s (%s, %.0fu, %s⛽)",
            step_index,
            total_steps,
            step['from'],
            step['to'],
            step['mode'],
            step['distance'],
            step['fuel_cost'],
        )

        if not self._ensure_valid_state(ship_controller, 'IN_ORBIT'):
            logger.error("Failed to transition to IN_ORBIT for navigation")
            return False

        success = ship_controller.navigate(
            waypoint=step['to'],
            flight_mode=step['mode'],
            auto_refuel=False,
        )

        if not success:
            logger.error(f"Navigation failed at step {step_index}")
            return False

        current_ship_data = self._get_status_or_log(
            ship_controller,
            "Failed to get ship status after navigation",
        )
        if not current_ship_data:
            return False

        actual_location = current_ship_data['nav']['waypointSymbol']
        actual_state = current_ship_data['nav']['status']

        if actual_location != step['to']:
            logger.error(f"Navigation error: expected {step['to']}, arrived at {actual_location}")
            return False

        if actual_state != 'IN_ORBIT':
            logger.warning(f"Unexpected state after navigation: {actual_state} (expected IN_ORBIT)")

        logger.info(f"✅ Arrived at {step['to']} (state: {actual_state})")

        if operation_controller:
            operation_controller.checkpoint({
                'completed_step': step_index,
                'location': actual_location,
                'fuel': current_ship_data['fuel']['current'],
                'state': actual_state,
            })

        return True

    def _execute_refuel_step(
        self,
        ship_controller,
        step: Dict,
        step_index: int,
        total_steps: int,
        operation_controller,
    ) -> bool:
        logger.info(
            "Step %s/%s: Refuel at %s (+%s⛽)",
            step_index,
            total_steps,
            step['waypoint'],
            step['fuel_added'],
        )

        current_ship_data = self._get_status_or_log(
            ship_controller,
            "Failed to get ship status before refuel",
        )
        if not current_ship_data:
            return False

        current_location = current_ship_data['nav']['waypointSymbol']
        refuel_waypoint = step['waypoint']

        if current_location != refuel_waypoint:
            logger.info(
                "Navigating to refuel waypoint: %s → %s",
                current_location,
                refuel_waypoint,
            )

            if not self._ensure_valid_state(ship_controller, 'IN_ORBIT'):
                logger.error("Failed to transition to IN_ORBIT for navigation to refuel stop")
                return False

            success = ship_controller.navigate(
                waypoint=refuel_waypoint,
                flight_mode='DRIFT',
                auto_refuel=False,
            )

            if not success:
                logger.error(f"Navigation to refuel waypoint {refuel_waypoint} failed")
                return False

            current_ship_data = self._get_status_or_log(
                ship_controller,
                "Failed to get ship status after navigation to refuel stop",
            )
            if not current_ship_data:
                return False

            actual_location = current_ship_data['nav']['waypointSymbol']
            if actual_location != refuel_waypoint:
                logger.error(f"Navigation error: expected {refuel_waypoint}, arrived at {actual_location}")
                return False

            logger.info(f"✅ Arrived at refuel stop {refuel_waypoint}")

        if not self._ensure_valid_state(ship_controller, 'DOCKED'):
            logger.error("Failed to dock for refueling")
            return False

        status = ship_controller.get_status()
        fuel_before = status['fuel']['current'] if status else 0

        if not ship_controller.refuel():
            logger.error("Refuel failed")
            return False

        status_after = ship_controller.get_status()
        fuel_after = status_after['fuel']['current'] if status_after else 0
        logger.info(f"✅ Refueled: {fuel_before} → {fuel_after}")

        if operation_controller:
            operation_controller.checkpoint({
                'completed_step': step_index,
                'location': refuel_waypoint,
                'fuel': fuel_after,
                'state': 'DOCKED',
            })

        return True

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
            prefer_cruise: Deprecated. Cruise is always preferred.

        Returns:
            True if navigation successful and arrived at destination, False otherwise
        """
        prefer_cruise = True  # Cruise preference enforced globally

        # Get current ship status
        ship_data = self._get_status_or_log(ship_controller, "Failed to get ship status")
        if not ship_data:
            return False

        # Validate ship health
        if routing_paused():
            details = get_pause_details() or {}
            logger.error("Routing paused: %s", details.get("reason", "Validation failure"))
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
        route = self.plan_route(ship_data, destination, prefer_cruise=True)
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
                logger.info(
                    "   %s. Navigate %s → %s (%s, %.0fu, %s⛽)",
                    i,
                    step['from'],
                    step['to'],
                    step['mode'],
                    step['distance'],
                    step['fuel_cost'],
                )
            elif step['action'] == 'refuel':
                logger.info(
                    "   %s. Refuel at %s (+%s⛽)",
                    i,
                    step['waypoint'],
                    step['fuel_added'],
                )

        # Execute each step with state machine
        start_step = 1

        # Resume from checkpoint if available
        if operation_controller and operation_controller.can_resume():
            checkpoint = operation_controller.get_last_checkpoint()
            if checkpoint:
                start_step = checkpoint.get('completed_step', 0) + 1
                logger.info(f"Resuming from step {start_step}/{len(route['steps'])}")

        total_steps = len(route['steps'])
        handlers = {
            'navigate': self._execute_navigate_step,
            'refuel': self._execute_refuel_step,
        }

        for i, step in enumerate(route['steps'], 1):
            # Skip already completed steps
            if i < start_step:
                continue

            if not self._should_continue_operation(operation_controller):
                return False

            handler = handlers.get(step['action'])
            if not handler:
                logger.warning("Skipping unsupported route action: %s", step['action'])
                continue

            if not handler(ship_controller, step, i, total_steps, operation_controller):
                return False

        # Final verification
        final_ship_data = self._get_status_or_log(
            ship_controller,
            "Could not verify final position",
        )
        if not final_ship_data:
            return True  # Navigation succeeded even if we can't verify

        final_location = final_ship_data['nav']['waypointSymbol']
        final_state = final_ship_data['nav']['status']
        final_fuel = final_ship_data['fuel']['current']

        if final_location != destination:
            logger.error(
                "Route execution failed: ended at %s, expected %s",
                final_location,
                destination,
            )
            return False

        logger.info(
            "✅ Route execution complete. Arrived at %s (state: %s, fuel: %s⛽)",
            destination,
            final_state,
            final_fuel,
        )

        self._maybe_proactive_refuel(
            ship_controller,
            destination,
            final_ship_data,
            final_state,
            final_fuel,
        )

        return True

    def _maybe_proactive_refuel(
        self,
        ship_controller,
        destination: str,
        final_ship_data: Dict,
        final_state: str,
        final_fuel: int,
    ) -> None:
        """Top up fuel at destination when necessary to keep cruise capability."""
        capacity = final_ship_data['fuel']['capacity']
        if final_fuel >= capacity * 0.75:
            return

        dest_wp = self.graph['waypoints'].get(destination, {})
        has_marketplace = 'MARKETPLACE' in dest_wp.get('traits', [])
        if not has_marketplace:
            return

        logger.info(
            "🔋 Proactive refuel: %s/%s fuel (maintaining CRUISE capability)",
            final_fuel,
            capacity,
        )

        if final_state != 'DOCKED' and not self._ensure_valid_state(ship_controller, 'DOCKED'):
            logger.warning("⚠️  Failed to dock for proactive refuel")
            return

        if ship_controller.refuel():
            after_refuel = ship_controller.get_status()
            if after_refuel:
                new_fuel = after_refuel['fuel']['current']
                logger.info(f"✅ Refueled: {final_fuel} → {new_fuel} fuel")
        else:
            logger.warning("⚠️  Proactive refuel failed")

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
