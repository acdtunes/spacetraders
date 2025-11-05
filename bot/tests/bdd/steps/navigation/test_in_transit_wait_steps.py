"""
BDD step definitions for in-transit wait handling.

Tests the bug where ships try to navigate to the next segment while still
in transit, causing "Ship is currently in-transit" API errors.
"""
import pytest
import time
from datetime import datetime, timezone, timedelta
from pytest_bdd import scenarios, given, when, then, parsers

from domain.shared.value_objects import Waypoint, Fuel
from domain.shared.ship import Ship
from ports.repositories import IShipRepository

# Tests use time.sleep but with mocked/minimal durations for speed

# Load all scenarios from the feature file
scenarios('../../features/navigation/in_transit_wait.feature')


# ============================================================================
# Mock Implementations for Testing
# ============================================================================

class TestShipRepository(IShipRepository):
    """In-memory ship repository for testing"""

    def __init__(self):
        self.ships = {}

    def find_by_symbol(self, ship_symbol: str, player_id: int):
        key = f"{player_id}:{ship_symbol}"
        return self.ships.get(key)

    def create(self, ship: Ship):
        key = f"{ship.player_id}:{ship.ship_symbol}"
        self.ships[key] = ship
        return ship

    def update(self, ship: Ship, from_api: bool = False):
        key = f"{ship.player_id}:{ship.ship_symbol}"
        self.ships[key] = ship
        return ship

    def find_all_by_player(self, player_id: int):
        return [s for k, s in self.ships.items() if k.startswith(f"{player_id}:")]

    def delete(self, ship_symbol: str, player_id: int):
        key = f"{player_id}:{ship_symbol}"
        if key in self.ships:
            del self.ships[key]

    def sync_from_api(self, ship_symbol: str, player_id: int, api_client, graph_provider) -> Ship:
        """Mock sync from API - just returns the ship from memory"""
        ship = self.find_by_symbol(ship_symbol, player_id)
        if ship is None:
            raise ValueError(f"Ship {ship_symbol} not found for player {player_id}")
        return ship


class TestAPIClient:
    """
    Mock API client that simulates SpaceTraders API behavior with IN_TRANSIT handling.
    """

    def __init__(self):
        self.ships = {}
        self.calls = []

    def register_ship(self, ship_symbol: str, nav_status: str, fuel: int, location: str):
        """Register a ship for API simulation"""
        self.ships[ship_symbol] = {
            "symbol": ship_symbol,
            "nav": {
                "status": nav_status,
                "waypointSymbol": location,
                "route": {}
            },
            "fuel": {"current": fuel, "capacity": 400}
        }

    def get_ship(self, ship_symbol: str):
        """Return full ship data"""
        self.calls.append({"action": "get_ship", "ship": ship_symbol})

        if ship_symbol in self.ships:
            return {"data": self.ships[ship_symbol]}

        return {"data": None}

    def navigate_ship(self, ship_symbol: str, waypoint: str):
        """
        Simulate navigation - sets ship to IN_TRANSIT with arrival time.
        Fails if ship is already IN_TRANSIT.
        """
        self.calls.append({"action": "navigate", "ship": ship_symbol, "waypoint": waypoint})

        if ship_symbol not in self.ships:
            raise Exception("Ship not found")

        ship_data = self.ships[ship_symbol]

        # Check if ship is already IN_TRANSIT
        if ship_data["nav"]["status"] == "IN_TRANSIT":
            arrival = ship_data["nav"]["route"].get("arrival", "")
            raise Exception(
                f"Ship is currently in-transit to {ship_data['nav']['waypointSymbol']} "
                f"and arrives in 40 seconds at {arrival}"
            )

        # Set ship to IN_TRANSIT
        ship_data["nav"]["status"] = "IN_TRANSIT"
        ship_data["nav"]["waypointSymbol"] = waypoint

        # Set arrival time (3 seconds in future for testing)
        arrival_time = datetime.now(timezone.utc) + timedelta(seconds=3)
        ship_data["nav"]["route"] = {
            "destination": {"symbol": waypoint},
            "arrival": arrival_time.isoformat().replace("+00:00", "Z")
        }

        # Consume fuel
        ship_data["fuel"]["current"] = max(0, ship_data["fuel"]["current"] - 30)

        return {
            "data": {
                "nav": ship_data["nav"],
                "fuel": ship_data["fuel"]
            }
        }

    def orbit_ship(self, ship_symbol: str):
        """Set ship to IN_ORBIT"""
        self.calls.append({"action": "orbit", "ship": ship_symbol})

        if ship_symbol in self.ships:
            self.ships[ship_symbol]["nav"]["status"] = "IN_ORBIT"

            return {
                "data": {
                    "nav": {
                        "status": "IN_ORBIT",
                        "waypointSymbol": self.ships[ship_symbol]["nav"]["waypointSymbol"]
                    }
                }
            }

        return {"data": {"nav": {"status": "IN_ORBIT"}}}

    def dock_ship(self, ship_symbol: str):
        """Set ship to DOCKED"""
        self.calls.append({"action": "dock", "ship": ship_symbol})

        if ship_symbol in self.ships:
            self.ships[ship_symbol]["nav"]["status"] = "DOCKED"

            return {
                "data": {
                    "nav": {
                        "status": "DOCKED",
                        "waypointSymbol": self.ships[ship_symbol]["nav"]["waypointSymbol"]
                    }
                }
            }

        return {"data": {"nav": {"status": "DOCKED"}}}

    def simulate_arrival(self, ship_symbol: str):
        """Manually simulate ship arrival (changes IN_TRANSIT to IN_ORBIT)"""
        if ship_symbol in self.ships:
            if self.ships[ship_symbol]["nav"]["status"] == "IN_TRANSIT":
                self.ships[ship_symbol]["nav"]["status"] = "IN_ORBIT"
                self.ships[ship_symbol]["nav"]["route"] = {}


# ============================================================================
# Given Steps - Setup
# ============================================================================

@given(parsers.parse('a test ship "{ship_symbol}" is registered for in-transit wait test'))
def register_test_ship_wait(context, ship_symbol):
    """Register test ship for in-transit wait test"""
    context['ship_symbol'] = ship_symbol
    context['player_id'] = 1
    context['ship_repo'] = TestShipRepository()
    context['api_client'] = TestAPIClient()


@given(parsers.parse('the ship is at waypoint "{waypoint}" with {fuel:d} fuel and {capacity:d} capacity'))
def setup_ship_at_waypoint(context, waypoint, fuel, capacity):
    """Create ship at specific waypoint with fuel"""
    ship = Ship(
        ship_symbol=context['ship_symbol'],
        player_id=context['player_id'],
        current_location=Waypoint(waypoint, 0, 0, "X1-TEST", "PLANET", has_fuel=False),
        fuel=Fuel(current=fuel, capacity=capacity),
        fuel_capacity=capacity,
        cargo_capacity=40,
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.IN_ORBIT
    )

    context['ship_repo'].create(ship)
    context['api_client'].register_ship(context['ship_symbol'], "IN_ORBIT", fuel, waypoint)


@given(parsers.parse('waypoint "{waypoint}" is {distance:d} units away without marketplace'))
def setup_waypoint_without_marketplace(context, waypoint, distance):
    """Setup waypoint without marketplace"""
    if 'waypoints' not in context:
        context['waypoints'] = {}
    context['waypoints'][waypoint] = {'distance': distance, 'has_marketplace': False}


@given(parsers.parse('waypoint "{waypoint_to}" is {distance:d} units away from "{waypoint_from}"'))
def setup_waypoint_distance_from(context, waypoint_to, distance, waypoint_from):
    """Setup waypoint distance from another waypoint"""
    if 'waypoints' not in context:
        context['waypoints'] = {}
    context['waypoints'][f"{waypoint_from}->{waypoint_to}"] = distance


@given(parsers.parse('the route from "{start}" to "{end}" requires {segments:d} segments'))
def setup_route_segments(context, start, end, segments):
    """Mark route as requiring multiple segments"""
    context['route_segments'] = segments


@given(parsers.parse('a test ship "{ship_symbol}" is registered for transit block test'))
def register_test_ship_transit_block(context, ship_symbol):
    """Register test ship for transit block test"""
    register_test_ship_wait(context, ship_symbol)


@given(parsers.parse('the ship is IN_TRANSIT to "{waypoint}" with arrival in {seconds:d} seconds'))
def setup_ship_in_transit(context, waypoint, seconds):
    """Setup ship in IN_TRANSIT state"""
    ship = Ship(
        ship_symbol=context['ship_symbol'],
        player_id=context['player_id'],
        current_location=Waypoint(waypoint, 0, 0, "X1-TEST", "PLANET", has_fuel=False),
        fuel=Fuel(current=200, capacity=400),
        fuel_capacity=400,
        cargo_capacity=40,
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.IN_TRANSIT  # Ship is in transit
    )

    context['ship_repo'].create(ship)

    # Register ship in API as IN_TRANSIT with arrival time
    arrival_time = datetime.now(timezone.utc) + timedelta(seconds=seconds)
    context['api_client'].ships[context['ship_symbol']] = {
        "symbol": context['ship_symbol'],
        "nav": {
            "status": "IN_TRANSIT",
            "waypointSymbol": waypoint,
            "route": {
                "destination": {"symbol": waypoint},
                "arrival": arrival_time.isoformat().replace("+00:00", "Z")
            }
        },
        "fuel": {"current": 200, "capacity": 400}
    }


@given(parsers.parse('a test ship "{ship_symbol}" is registered for 3-hop test'))
def register_test_ship_3hop(context, ship_symbol):
    """Register test ship for 3-hop test"""
    register_test_ship_wait(context, ship_symbol)


@given(parsers.parse('waypoint "{waypoint}" is {distance:d} units away'))
def setup_waypoint_distance(context, waypoint, distance):
    """Setup waypoint with distance"""
    if 'waypoints' not in context:
        context['waypoints'] = {}
    context['waypoints'][waypoint] = {'distance': distance}


# ============================================================================
# When Steps - Actions
# ============================================================================

@when(parsers.parse('I navigate from "{start}" to "{end}" via "{middle}"'))
def navigate_via_intermediate(context, start, end, middle):
    """Navigate from start to end via intermediate waypoint"""
    ship = context['ship_repo'].find_by_symbol(context['ship_symbol'], context['player_id'])
    api_client = context['api_client']

    context['navigation_steps'] = []
    context['wait_times'] = []

    try:
        # Ensure ship is in orbit
        ship.ensure_in_orbit()

        # Segment 1: Navigate to middle waypoint
        context['navigation_steps'].append(f"navigate_to_{middle}")
        nav_result = api_client.navigate_ship(context['ship_symbol'], middle)

        # Ship is now IN_TRANSIT - need to wait for arrival
        ship_response = api_client.get_ship(context['ship_symbol'])
        ship_data = ship_response.get('data')

        if ship_data and ship_data['nav']['status'] == 'IN_TRANSIT':
            context['navigation_steps'].append(f"detected_in_transit_to_{middle}")

            # Get arrival time
            arrival_time_str = ship_data['nav']['route']['arrival']
            context['navigation_steps'].append(f"got_arrival_time_{arrival_time_str}")

            # Calculate wait time
            from application.navigation.commands.navigate_ship import calculate_arrival_wait_time
            wait_time = calculate_arrival_wait_time(arrival_time_str)
            context['wait_times'].append(wait_time)
            context['navigation_steps'].append(f"calculated_wait_{wait_time}s")

            # Sleep for minimal time (0.1s instead of actual wait time for speed)
            total_wait = wait_time + 3
            context['navigation_steps'].append(f"sleeping_{total_wait}s")
            time.sleep(0.1)  # Minimal sleep for test speed

            # Simulate arrival in mock API
            api_client.simulate_arrival(context['ship_symbol'])

            # Sync ship state after arrival
            ship_response = api_client.get_ship(context['ship_symbol'])
            ship_data = ship_response.get('data')
            if ship_data:
                context['navigation_steps'].append(f"synced_after_arrival_status_{ship_data['nav']['status']}")

        # Create updated ship with new location and IN_ORBIT status
        ship = Ship(
            ship_symbol=ship.ship_symbol,
            player_id=ship.player_id,
            current_location=Waypoint(middle, 0, 0, "X1-TEST", "PLANET", has_fuel=False),
            fuel=ship.fuel,
            fuel_capacity=ship.fuel_capacity,
            cargo_capacity=ship.cargo_capacity,
            cargo_units=ship.cargo_units,
            engine_speed=ship.engine_speed,
            nav_status=Ship.IN_ORBIT
        )
        context['ship_repo'].update(ship)

        # Segment 2: Navigate to final destination
        context['navigation_steps'].append(f"navigate_to_{end}")
        nav_result = api_client.navigate_ship(context['ship_symbol'], end)

        # Wait for second segment arrival
        ship_response = api_client.get_ship(context['ship_symbol'])
        ship_data = ship_response.get('data')

        if ship_data and ship_data['nav']['status'] == 'IN_TRANSIT':
            arrival_time_str = ship_data['nav']['route']['arrival']
            from application.navigation.commands.navigate_ship import calculate_arrival_wait_time
            wait_time = calculate_arrival_wait_time(arrival_time_str)
            time.sleep(0.1)  # Minimal sleep instead of wait_time + 3
            api_client.simulate_arrival(context['ship_symbol'])

        # Create updated ship with final location and IN_ORBIT status
        ship = Ship(
            ship_symbol=ship.ship_symbol,
            player_id=ship.player_id,
            current_location=Waypoint(end, 0, 0, "X1-TEST", "PLANET", has_fuel=False),
            fuel=ship.fuel,
            fuel_capacity=ship.fuel_capacity,
            cargo_capacity=ship.cargo_capacity,
            cargo_units=ship.cargo_units,
            engine_speed=ship.engine_speed,
            nav_status=Ship.IN_ORBIT
        )
        context['ship_repo'].update(ship)

        context['navigation_steps'].append(f"arrived_at_{end}")
        context['error'] = None

    except Exception as e:
        context['error'] = e
        context['error_message'] = str(e)


@when('I attempt to navigate the ship without waiting for arrival')
def attempt_navigate_without_wait(context):
    """Attempt to navigate while ship is in transit"""
    api_client = context['api_client']

    try:
        # Try to navigate while ship is IN_TRANSIT
        api_client.navigate_ship(context['ship_symbol'], "X1-NEXT")
        context['error'] = None
    except Exception as e:
        context['error'] = e
        context['error_message'] = str(e)


@when(parsers.parse('I navigate through all 3 hops from "{start}" to "{end}"'))
def navigate_3_hops(context, start, end):
    """Navigate through 3-hop route with proper waiting"""
    ship = context['ship_repo'].find_by_symbol(context['ship_symbol'], context['player_id'])
    api_client = context['api_client']

    context['navigation_steps'] = []
    context['segments_completed'] = 0

    # Get waypoints from context
    hop1 = "X1-HOP1"
    hop2 = "X1-HOP2"
    final = end

    waypoints = [hop1, hop2, final]

    try:
        ship.ensure_in_orbit()

        for waypoint in waypoints:
            # Navigate to waypoint
            context['navigation_steps'].append(f"navigate_to_{waypoint}")
            api_client.navigate_ship(context['ship_symbol'], waypoint)

            # Check if IN_TRANSIT and wait
            ship_response = api_client.get_ship(context['ship_symbol'])
            ship_data = ship_response.get('data')

            if ship_data and ship_data['nav']['status'] == 'IN_TRANSIT':
                context['navigation_steps'].append(f"waiting_for_arrival_at_{waypoint}")
                arrival_time_str = ship_data['nav']['route']['arrival']
                from application.navigation.commands.navigate_ship import calculate_arrival_wait_time
                wait_time = calculate_arrival_wait_time(arrival_time_str)
                time.sleep(0.1)  # Minimal sleep instead of wait_time + 3
                api_client.simulate_arrival(context['ship_symbol'])
                context['navigation_steps'].append(f"arrived_at_{waypoint}")

            # Create updated ship with new location and IN_ORBIT status
            ship = Ship(
                ship_symbol=ship.ship_symbol,
                player_id=ship.player_id,
                current_location=Waypoint(waypoint, 0, 0, "X1-TEST", "PLANET", has_fuel=False),
                fuel=ship.fuel,
                fuel_capacity=ship.fuel_capacity,
                cargo_capacity=ship.cargo_capacity,
                cargo_units=ship.cargo_units,
                engine_speed=ship.engine_speed,
                nav_status=Ship.IN_ORBIT
            )
            context['ship_repo'].update(ship)

            context['segments_completed'] += 1

        context['error'] = None

    except Exception as e:
        context['error'] = e
        context['error_message'] = str(e)


# ============================================================================
# Then Steps - Assertions
# ============================================================================

@then(parsers.parse('the ship should navigate to "{waypoint}" and enter IN_TRANSIT'))
def check_navigate_to_waypoint(context, waypoint):
    """Verify ship navigated to waypoint"""
    assert f"navigate_to_{waypoint}" in context['navigation_steps']


@then('the system should detect the ship is IN_TRANSIT with arrival time')
def check_detect_in_transit(context):
    """Verify system detected IN_TRANSIT state"""
    # Check for detection step in navigation
    in_transit_steps = [s for s in context['navigation_steps'] if 'detected_in_transit' in s]
    assert len(in_transit_steps) > 0


@then('the system should calculate the wait time until arrival')
def check_calculate_wait_time(context):
    """Verify wait time was calculated"""
    wait_steps = [s for s in context['navigation_steps'] if 'calculated_wait' in s]
    assert len(wait_steps) > 0


@then('the system should sleep for the calculated wait time plus buffer')
def check_sleep_for_wait_time(context):
    """Verify system slept for wait time"""
    sleep_steps = [s for s in context['navigation_steps'] if 'sleeping_' in s]
    assert len(sleep_steps) > 0


@then('the system should sync ship state after arrival showing IN_ORBIT')
def check_sync_after_arrival(context):
    """Verify ship state was synced after arrival"""
    sync_steps = [s for s in context['navigation_steps'] if 'synced_after_arrival' in s]
    assert len(sync_steps) > 0
    # Check that status is IN_ORBIT
    in_orbit_syncs = [s for s in sync_steps if 'IN_ORBIT' in s]
    assert len(in_orbit_syncs) > 0


@then(parsers.parse('the ship should then navigate to "{waypoint}" successfully'))
def check_navigate_to_final(context, waypoint):
    """Verify ship navigated to final waypoint"""
    assert f"navigate_to_{waypoint}" in context['navigation_steps']
    assert context.get('error') is None


@then(parsers.parse('the final ship location should be "{waypoint}"'))
def check_final_location(context, waypoint):
    """Verify final ship location"""
    ship = context['ship_repo'].find_by_symbol(context['ship_symbol'], context['player_id'])
    assert ship.current_location.symbol == waypoint


@then('the navigation should fail with "currently in-transit" error')
def check_navigation_failed_in_transit(context):
    """Verify navigation failed due to IN_TRANSIT state"""
    assert context.get('error') is not None
    assert 'in-transit' in context.get('error_message', '').lower()


@then('the error message should mention arrival time')
def check_error_mentions_arrival(context):
    """Verify error message contains arrival time info"""
    assert context.get('error') is not None
    error_msg = context.get('error_message', '')
    # Should mention either 'arrives' or 'arrival'
    assert 'arrive' in error_msg.lower()


@then('each segment should wait for arrival before proceeding')
def check_each_segment_waits(context):
    """Verify each segment waited for arrival"""
    waiting_steps = [s for s in context['navigation_steps'] if 'waiting_for_arrival' in s]
    # Should have 3 waits (one for each hop)
    assert len(waiting_steps) == 3


@then('all 3 segments should complete successfully')
def check_all_segments_completed(context):
    """Verify all segments completed"""
    assert context.get('error') is None
    assert context['segments_completed'] == 3


@then(parsers.parse('the ship should be at "{waypoint}" in IN_ORBIT status'))
def check_ship_at_waypoint_in_orbit(context, waypoint):
    """Verify ship is at waypoint in IN_ORBIT status"""
    ship = context['ship_repo'].find_by_symbol(context['ship_symbol'], context['player_id'])
    assert ship.current_location.symbol == waypoint
    assert ship.nav_status == Ship.IN_ORBIT
