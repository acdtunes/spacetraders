"""BDD steps for daemon event loop responsiveness testing"""
import asyncio
import time
import threading
from datetime import datetime
from pathlib import Path
from pytest_bdd import scenarios, given, when, then, parsers
from spacetraders.adapters.primary.daemon.daemon_server import DaemonServer
from spacetraders.adapters.primary.daemon.daemon_client import DaemonClient
from spacetraders.configuration.container import (
    get_player_repository,
    get_ship_repository,
    reset_container
)
from spacetraders.domain.shared.player import Player
from spacetraders.domain.shared.ship import Ship
from spacetraders.domain.shared.value_objects import Waypoint, Fuel, FlightMode

scenarios('../../features/daemon/event_loop_responsiveness.feature')


@given("the daemon server is running")
def daemon_running(context):
    """Start daemon server for test"""
    reset_container()

    socket_path = Path("var/daemon.sock")
    if socket_path.exists():
        socket_path.unlink()

    server = DaemonServer()
    context['daemon_server'] = server

    # Start server in background thread with its own event loop
    def run_server():
        loop = asyncio.new_event_loop()
        asyncio.set_event_loop(loop)
        context['daemon_loop'] = loop
        context['daemon_error'] = None
        try:
            loop.run_until_complete(server.start())
        except Exception as e:
            context['daemon_error'] = str(e)
            import traceback
            context['daemon_traceback'] = traceback.format_exc()
        finally:
            loop.close()

    thread = threading.Thread(target=run_server, daemon=True)
    thread.start()
    context['daemon_thread'] = thread

    # Wait for socket to exist
    timeout = 5
    start = time.time()
    while not socket_path.exists() and time.time() - start < timeout:
        time.sleep(0.1)

    if context.get('daemon_error'):
        raise AssertionError(
            f"Daemon failed to start: {context['daemon_error']}\n"
            f"{context.get('daemon_traceback', '')}"
        )

    assert socket_path.exists(), "Daemon socket did not appear"
    context['daemon_client'] = DaemonClient()


@given(parsers.parse('a player exists with agent "{agent_symbol}"'))
def player_exists(context, agent_symbol):
    """Create a test player"""
    player_repo = get_player_repository()

    player = Player(
        player_id=None,  # Will be assigned by repository
        agent_symbol=agent_symbol,
        token=f"token-{agent_symbol}",
        created_at=datetime.now(),
        last_active=datetime.now(),
        metadata={}
    )
    created_player = player_repo.create(player)
    context['player'] = created_player


@given(parsers.parse('a ship "{ship_symbol}" exists at waypoint "{waypoint_symbol}" in orbit'))
def ship_exists_in_orbit(context, ship_symbol, waypoint_symbol):
    """Create a test ship in orbit"""
    ship_repo = get_ship_repository()

    ship = Ship(
        ship_symbol=ship_symbol,
        player_id=context['player'].player_id,
        current_location=Waypoint(symbol=waypoint_symbol, x=0, y=0),
        fuel=Fuel(current=100, capacity=100),
        fuel_capacity=100,
        cargo_capacity=50,
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.IN_ORBIT
    )
    ship_repo.create(ship)
    context['ship'] = ship


@given(parsers.parse('a ship "{ship_symbol}" exists at waypoint "{waypoint_symbol}" docked'))
def ship_exists_docked(context, ship_symbol, waypoint_symbol):
    """Create a test ship docked"""
    ship_repo = get_ship_repository()

    ship = Ship(
        ship_symbol=ship_symbol,
        player_id=context['player'].player_id,
        current_location=Waypoint(symbol=waypoint_symbol, x=0, y=0),
        fuel=Fuel(current=20, capacity=100),
        fuel_capacity=100,
        cargo_capacity=50,
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.DOCKED
    )
    ship_repo.create(ship)
    context['ship'] = ship


@given(parsers.parse('waypoint "{waypoint_symbol}" exists in the same system'))
def waypoint_exists(context, waypoint_symbol):
    """Record destination waypoint (no actual creation needed for this test)"""
    context['destination_waypoint'] = waypoint_symbol


@given("the ship has sufficient fuel for the journey")
def ship_has_fuel(context):
    """Verify ship has fuel (already set in ship creation)"""
    assert context['ship'].fuel.current > 0


@given("the ship has low fuel requiring refuel")
def ship_has_low_fuel(context):
    """Ship already created with low fuel (20/100)"""
    assert context['ship'].fuel.current < 50


@given(parsers.parse('waypoint "{waypoint_symbol}" has a fuel station'))
def waypoint_has_fuel_station(context, waypoint_symbol):
    """Mark waypoint as having fuel station (for test purposes)"""
    context['fuel_station_waypoint'] = waypoint_symbol


@when(parsers.parse('I create a navigation container for ship "{ship_symbol}" to waypoint "{destination}"'))
def create_navigation_container(context, ship_symbol, destination):
    """Create a command container that executes NavigateShipCommand"""
    client = context['daemon_client']

    # Create container with NavigateShipCommand
    container_config = {
        'container_id': f'nav-{ship_symbol}',
        'player_id': context['player'].player_id,
        'container_type': 'command',
        'config': {
            'command_type': 'NavigateShipCommand',
            'params': {
                'ship_symbol': ship_symbol,
                'destination_waypoint_symbol': destination,
                'player_id': context['player'].player_id,
                'use_api': False  # Use mock API to avoid external calls
            },
            'iterations': 1
        }
    }

    result = client.create_container(container_config)
    context['container_id'] = result['container_id']


@when("I wait for the container to start sleeping during transit")
def wait_for_transit_sleep(context):
    """Wait for container to enter sleep phase during transit

    The navigation handler will:
    1. Start the container
    2. Enter NavigateShipHandler.handle()
    3. Call API to start navigation
    4. Enter time.sleep() to wait for arrival

    We need to give it enough time to enter the sleep but not complete it.
    A short sleep here ensures the container has started processing.
    """
    time.sleep(0.5)  # Give container time to start and enter sleep phase


@when("I wait for the container to start sleeping during refuel")
def wait_for_refuel_sleep(context):
    """Wait for container to enter sleep phase during refuel wait"""
    time.sleep(0.5)  # Give container time to start and enter sleep phase


@then("I should be able to inspect the container within 1 second")
def inspect_container_within_timeout(context):
    """Verify daemon responds to inspect request within 1 second

    This is the critical test:
    - If time.sleep() is used: daemon blocks and this will timeout
    - If asyncio.sleep() is used: daemon remains responsive and returns quickly
    """
    client = context['daemon_client']
    container_id = context['container_id']

    start_time = time.time()

    try:
        # Set socket timeout to 1 second
        import socket
        original_timeout = socket.getdefaulttimeout()
        socket.setdefaulttimeout(1.0)

        result = client.inspect_container(container_id)
        elapsed = time.time() - start_time

        context['inspect_result'] = result
        context['inspect_elapsed'] = elapsed

        # Restore original timeout
        socket.setdefaulttimeout(original_timeout)

        # Assert response came within 1 second
        assert elapsed < 1.0, (
            f"Daemon took {elapsed:.2f}s to respond, expected < 1.0s. "
            f"This indicates event loop blocking with time.sleep()."
        )

    except socket.timeout:
        socket.setdefaulttimeout(original_timeout)
        raise AssertionError(
            "Daemon did not respond within 1 second. "
            "Event loop is blocked by time.sleep() in navigation handler."
        )


@then(parsers.parse('the container should show status "{expected_status}"'))
def verify_container_status(context, expected_status):
    """Verify container status from inspect result"""
    result = context.get('inspect_result')
    assert result is not None, "No inspect result available"

    actual_status = result.get('status')
    assert actual_status == expected_status, (
        f"Expected container status '{expected_status}', got '{actual_status}'"
    )
