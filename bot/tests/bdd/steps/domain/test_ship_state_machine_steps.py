"""Step definitions for ship state machine"""
from pytest_bdd import scenarios, given, when, then, parsers
import pytest

from domain.shared.ship import Ship, InvalidNavStatusError
from domain.shared.value_objects import Waypoint, Fuel

scenarios('../../features/domain/ship_state_machine.feature')


@given(parsers.parse('a ship with the following state:\n{table}'), target_fixture='ship')
@given('a ship with the following state:', target_fixture='ship')
def create_ship(context):
    """Create a ship entity for testing"""
    # Default values for testing
    waypoint = Waypoint(
        symbol="X1-TEST-A1",
        waypoint_type="PLANET",
        x=0,
        y=0,
        system_symbol="X1-TEST",
        traits=(),
        has_fuel=True
    )

    ship = Ship(
        ship_symbol="TEST-SHIP-1",
        player_id=1,
        current_location=waypoint,
        fuel=Fuel(current=400, capacity=400),
        fuel_capacity=400,
        cargo_capacity=40,
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.DOCKED  # Default status
    )

    context['ship'] = ship
    return ship


@given(parsers.parse('the ship is in status "{status}"'))
def set_ship_status(context, status):
    """Set ship to specific nav status"""
    ship = context['ship']

    # Directly set the internal state for testing
    # (In production, state changes happen through methods)
    ship._nav_status = status
    context['ship'] = ship


@when("the ship attempts to orbit")
def attempt_orbit(context):
    """Attempt to call ensure_in_orbit on the ship"""
    ship = context['ship']
    try:
        ship.ensure_in_orbit()
        context['error'] = None
        context['result'] = 'success'
    except InvalidNavStatusError as e:
        context['error'] = e
        context['result'] = 'error'


@when("the ship attempts to dock")
def attempt_dock(context):
    """Attempt to call ensure_docked on the ship"""
    ship = context['ship']
    try:
        ship.ensure_docked()
        context['error'] = None
        context['result'] = 'success'
    except InvalidNavStatusError as e:
        context['error'] = e
        context['result'] = 'error'


@when("the ship departs to orbit")
def ship_departs(context):
    """Ship departs from docked to orbit"""
    ship = context['ship']
    try:
        ship.depart()
        context['error'] = None
    except InvalidNavStatusError as e:
        context['error'] = e


@when("the ship docks")
def ship_docks(context):
    """Ship docks from orbit"""
    ship = context['ship']
    try:
        ship.dock()
        context['error'] = None
    except InvalidNavStatusError as e:
        context['error'] = e


@when("the ship arrives at destination")
def ship_arrives(context):
    """Ship arrives from transit"""
    ship = context['ship']
    try:
        ship.arrive()
        context['error'] = None
    except InvalidNavStatusError as e:
        context['error'] = e


@then(parsers.parse('the operation should fail with "{message}"'))
def verify_error(context, message):
    """Verify operation failed with expected error"""
    assert context['result'] == 'error', "Operation should have failed"
    assert context['error'] is not None, "Should have an error"
    assert message.lower() in str(context['error']).lower(), \
        f"Expected '{message}' in error, got: {context['error']}"


@then(parsers.parse('the ship should be in status "{status}"'))
def verify_ship_status(context, status):
    """Verify ship is in expected status"""
    ship = context['ship']
    assert ship.nav_status == status, \
        f"Expected status {status}, got {ship.nav_status}"
    assert context.get('error') is None, f"Should not have errors: {context.get('error')}"
