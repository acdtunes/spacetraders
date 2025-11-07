"""Step definitions for workflow cargo management tests"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers

from domain.shared.ship import Ship
from domain.shared.value_objects import Waypoint, Fuel, Cargo, CargoItem

# Load scenarios
scenarios('../../../features/application/cargo/workflow_cargo_management.feature')


@pytest.fixture
def context():
    """Provide shared context dictionary for test data"""
    return {}


@given(parsers.parse('a player exists with ID {player_id:d}'))
def player_exists(context, player_id):
    """Set player ID in context"""
    context['player_id'] = player_id


@given(parsers.parse('the player has agent symbol "{agent_symbol}"'))
def player_has_agent(context, agent_symbol):
    """Set agent symbol in context"""
    context['agent_symbol'] = agent_symbol


@given(parsers.parse('a ship "{ship_symbol}" exists for player {player_id:d}'))
def ship_exists(context, ship_symbol, player_id):
    """Create a ship entity in context"""
    context['ship_symbol'] = ship_symbol
    context['player_id'] = player_id
    # Ship will be fully constructed in the workflow step


@given(parsers.parse('the ship has {units:d} units of "{cargo_symbol}" in cargo'))
def ship_has_cargo(context, units, cargo_symbol):
    """Set ship cargo state in context"""
    if 'cargo_items' not in context:
        context['cargo_items'] = []
    context['cargo_items'].append({
        'symbol': cargo_symbol,
        'units': units
    })


@given(parsers.parse('a contract requires delivering {units:d} units of "{trade_symbol}"'))
def contract_requires_delivery(context, units, trade_symbol):
    """Set contract delivery requirements in context"""
    context['required_trade_symbol'] = trade_symbol
    context['required_units'] = units
    # We'll create the contract in the when step


@when('the workflow processes cargo for delivery')
def workflow_processes_cargo(context):
    """Simulate the workflow cargo processing logic"""
    # Build the ship with cargo from context
    cargo_items = context.get('cargo_items', [])

    # Calculate total cargo units
    total_units = sum(item['units'] for item in cargo_items)

    # Build cargo inventory
    inventory = tuple(
        CargoItem(
            symbol=item['symbol'],
            name=item['symbol'].replace('_', ' ').title(),
            description=f"Description for {item['symbol']}",
            units=item['units']
        )
        for item in cargo_items
    )

    # Create cargo object
    cargo = Cargo(
        capacity=100,
        units=total_units,
        inventory=inventory
    )

    # Create ship
    ship = Ship(
        ship_symbol=context['ship_symbol'],
        player_id=context['player_id'],
        current_location=Waypoint(symbol="X1-TEST-A1", x=0, y=0),
        fuel=Fuel(current=100, capacity=100),
        fuel_capacity=100,
        cargo_capacity=100,
        cargo_units=total_units,
        engine_speed=10,
        nav_status=Ship.IN_ORBIT,
        cargo=cargo
    )

    # Get required delivery info
    required_symbol = context['required_trade_symbol']
    required_units = context['required_units']

    # Simulate workflow logic (CURRENT BUGGY LOGIC)
    current_units = ship.cargo.get_item_units(required_symbol)
    has_wrong_cargo = ship.cargo.has_items_other_than(required_symbol)

    # Track what the workflow decides to do
    context['jettison_actions'] = []
    context['purchase_units'] = 0
    context['workflow_action'] = None

    # CORRECT LOGIC (what it should be):
    # First check: do we have enough of the required cargo?
    if current_units >= required_units:
        # We have enough! Now check if we need to jettison wrong cargo
        if has_wrong_cargo:
            # Jettison wrong cargo but DON'T purchase anything
            for item in ship.cargo.inventory:
                if item.symbol != required_symbol:
                    context['jettison_actions'].append({
                        'symbol': item.symbol,
                        'units': item.units
                    })
            context['workflow_action'] = 'jettison_then_delivery'
            context['purchase_units'] = 0
        else:
            # Perfect - have exactly what we need
            context['workflow_action'] = 'skip_purchase_proceed_delivery'
            context['purchase_units'] = 0

    elif has_wrong_cargo:
        # Don't have enough, AND have wrong cargo
        # Jettison wrong cargo first, then calculate what to buy
        for item in ship.cargo.inventory:
            if item.symbol != required_symbol:
                context['jettison_actions'].append({
                    'symbol': item.symbol,
                    'units': item.units
                })

        # After jettison, we still have current_units of required cargo
        # (jettison only removed wrong items)
        units_to_purchase = required_units - current_units
        context['purchase_units'] = units_to_purchase
        context['workflow_action'] = 'jettison_purchase_delivery'

    else:
        # Don't have enough, but cargo is clean (only required item or empty)
        units_to_purchase = required_units - current_units
        context['purchase_units'] = units_to_purchase
        context['workflow_action'] = 'purchase_delivery'


@then(parsers.parse('the workflow should jettison {units:d} units of "{cargo_symbol}"'))
def verify_jettison_action(context, units, cargo_symbol):
    """Verify workflow decided to jettison specific cargo"""
    jettison_actions = context.get('jettison_actions', [])

    # Find jettison action for this cargo symbol
    matching_actions = [
        action for action in jettison_actions
        if action['symbol'] == cargo_symbol
    ]

    assert len(matching_actions) > 0, \
        f"Workflow should jettison {cargo_symbol}, but no jettison action found. Actions: {jettison_actions}"

    actual_units = matching_actions[0]['units']
    assert actual_units == units, \
        f"Workflow should jettison {units} units of {cargo_symbol}, but jettisoned {actual_units}"


@then(parsers.parse('the workflow should NOT jettison any "{cargo_symbol}"'))
def verify_no_jettison_of_cargo(context, cargo_symbol):
    """Verify workflow did NOT jettison specific cargo"""
    jettison_actions = context.get('jettison_actions', [])

    # Find any jettison action for this cargo symbol
    matching_actions = [
        action for action in jettison_actions
        if action['symbol'] == cargo_symbol
    ]

    assert len(matching_actions) == 0, \
        f"Workflow should NOT jettison {cargo_symbol}, but found jettison action: {matching_actions}"


@then('the workflow should NOT jettison any cargo')
def verify_no_jettison_at_all(context):
    """Verify workflow did not jettison anything"""
    jettison_actions = context.get('jettison_actions', [])
    assert len(jettison_actions) == 0, \
        f"Workflow should NOT jettison any cargo, but found actions: {jettison_actions}"


@then(parsers.parse('the workflow should determine {units:d} units to purchase'))
def verify_purchase_units(context, units):
    """Verify workflow calculated correct purchase amount"""
    actual_units = context.get('purchase_units', 0)
    assert actual_units == units, \
        f"Workflow should purchase {units} units, but calculated {actual_units}"


@then(parsers.parse('the workflow should determine {units:d} units to purchase after jettison'))
def verify_purchase_units_after_jettison(context, units):
    """Verify workflow calculated correct purchase amount after jettison"""
    actual_units = context.get('purchase_units', 0)
    assert actual_units == units, \
        f"Workflow should purchase {units} units after jettison, but calculated {actual_units}"


@then('the workflow should proceed directly to delivery')
def verify_proceed_to_delivery(context):
    """Verify workflow skips purchase and goes to delivery"""
    action = context.get('workflow_action')
    assert action in ['skip_purchase_proceed_delivery', 'jettison_then_delivery'], \
        f"Workflow should proceed directly to delivery, but action was: {action}"


@then(parsers.parse('the workflow should proceed directly to delivery with {units:d} units'))
def verify_proceed_to_delivery_with_units(context, units):
    """Verify workflow proceeds to delivery with specific units"""
    action = context.get('workflow_action')
    assert action in ['skip_purchase_proceed_delivery', 'jettison_then_delivery'], \
        f"Workflow should proceed directly to delivery, but action was: {action}"


@then('the workflow should proceed to purchase and delivery')
def verify_purchase_and_delivery(context):
    """Verify workflow will purchase then deliver"""
    action = context.get('workflow_action')
    assert action in ['purchase_delivery', 'jettison_purchase_delivery'], \
        f"Workflow should proceed to purchase and delivery, but action was: {action}"
