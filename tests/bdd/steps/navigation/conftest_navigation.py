"""Shared fixtures and steps for daemon tests"""
import pytest
import asyncio
from pytest_bdd import given, when, then, parsers
from datetime import datetime

from spacetraders.configuration.container import (
    get_database,
    get_player_repository,
    get_ship_repository,
    reset_container
)
from spacetraders.domain.shared.player import Player
from spacetraders.domain.shared.ship import Ship, Fuel, Nav, NavStatus
from spacetraders.domain.shared.value_objects import Waypoint


@pytest.fixture
def context():
    """Shared context dictionary for passing data between test steps"""
    return {}


@pytest.fixture(autouse=True)
def reset_test_environment():
    """Reset all singletons and database state before each test"""
    # Reset dependency injection container
    reset_container()

    # Get fresh database instance
    db = get_database()

    # Clear ship assignments table
    with db.transaction() as conn:
        cursor = conn.cursor()
        cursor.execute("DELETE FROM ship_assignments")
        cursor.execute("DELETE FROM container_logs")

    yield

    # Cleanup after test
    reset_container()


@pytest.fixture
def event_loop():
    """Create event loop for async tests"""
    loop = asyncio.get_event_loop_policy().new_event_loop()
    yield loop
    loop.close()


# ============================================================================
# SHARED STEP DEFINITIONS
# ============================================================================

@given(parsers.parse('a player exists with ID {player_id:d} and token "{token}"'))
def create_test_player(context, player_id, token):
    """Create a test player in the database"""
    player_repo = get_player_repository()

    # Create player
    player = Player(
        player_id=None,  # Will be assigned by repository
        agent_symbol=f"TEST-AGENT-{player_id}",
        token=token,
        created_at=datetime.now(),
        last_active=datetime.now(),
        metadata={}
    )

    created_player = player_repo.create(player)
    context['player_id'] = created_player.player_id
    context['player'] = created_player

    # If specific ID requested, update it
    if player_id != created_player.player_id:
        db = get_database()
        with db.transaction() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                UPDATE players SET player_id = ? WHERE player_id = ?
            """, (player_id, created_player.player_id))
        context['player_id'] = player_id


@given(parsers.parse('a ship "{ship_symbol}" exists for player {player_id:d} at "{location}"'))
@given(parsers.parse('a ship "{ship_symbol}" exists for player {player_id:d}'))
def create_test_ship(context, ship_symbol, player_id, location="X1-TEST-A1"):
    """Create a test ship in the database"""
    ship_repo = get_ship_repository()

    # Create waypoint for location
    waypoint = Waypoint(
        symbol=location,
        waypoint_type="PLANET",
        x=0,
        y=0,
        system_symbol="X1-TEST",
        traits=(),
        has_fuel=True
    )

    # Create ship
    ship = Ship(
        ship_symbol=ship_symbol,
        player_id=player_id,
        current_location=waypoint,
        fuel=Fuel(current=400, capacity=400),
        cargo_capacity=40,
        cargo_units=0,
        engine_speed=30,
        nav=Nav(
            status=NavStatus.DOCKED,
            waypoint_symbol=location,
            origin_symbol=location,
            destination_symbol=location
        )
    )

    # Save to database
    ship_repo.create(ship)

    context['ship_symbol'] = ship_symbol
    context['ship'] = ship
    context['player_id'] = player_id


@given('the daemon server is running')
def daemon_server_running(context):
    """Mark that daemon server is available for tests"""
    context['daemon_running'] = True
    # In real tests, we'd start actual daemon
    # For unit tests, we mock the interactions


@when(parsers.parse('I create a container for ship "{ship_symbol}"'))
def create_container(context, ship_symbol):
    """Create a container (mocked for unit tests)"""
    context['container_id'] = f"test-container-{ship_symbol}"


# ============================================================================
# ASSERTION HELPERS
# ============================================================================

def assert_ship_assignment(player_id: int, ship_symbol: str, expected_status: str):
    """Helper to assert ship assignment status"""
    db = get_database()
    with db.connection() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            SELECT status, release_reason FROM ship_assignments
            WHERE ship_symbol = ? AND player_id = ?
            ORDER BY assigned_at DESC LIMIT 1
        """, (ship_symbol, player_id))

        row = cursor.fetchone()
        assert row is not None, f"No assignment found for {ship_symbol}"
        assert row['status'] == expected_status, f"Expected {expected_status}, got {row['status']}"
        return row


def assert_ship_in_database(ship_symbol: str, player_id: int) -> dict:
    """Helper to assert ship exists and return its data"""
    db = get_database()
    with db.connection() as conn:
        cursor = conn.cursor()
        cursor.execute("""
            SELECT * FROM ships
            WHERE ship_symbol = ? AND player_id = ?
        """, (ship_symbol, player_id))

        row = cursor.fetchone()
        assert row is not None, f"Ship {ship_symbol} not found in database"
        return dict(row)


# ============================================================================
# CONTEXT HELPERS
# ============================================================================

def get_context_ship(context) -> Ship:
    """Get ship from context or database"""
    if 'ship' in context:
        return context['ship']

    ship_repo = get_ship_repository()
    ship = ship_repo.find_by_symbol(
        context['ship_symbol'],
        context['player_id']
    )
    assert ship is not None, "Ship not found"
    return ship


def get_context_player_id(context) -> int:
    """Get player_id from context"""
    assert 'player_id' in context, "player_id not in context"
    return context['player_id']
