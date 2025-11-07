"""Step definitions for cargo value objects tests"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers

from domain.shared.value_objects import CargoItem, Cargo

# Load scenarios from feature file
scenarios('../../features/domain/cargo_value_objects.feature')


@pytest.fixture
def context():
    """Shared context for test scenarios"""
    return {}


# CargoItem creation tests

@when(parsers.parse('I create a cargo item with symbol "{symbol}", name "{name}", description "{description}", and {units:d} units'))
def create_cargo_item(context, symbol, name, description, units):
    """Create a cargo item"""
    context['cargo_item'] = CargoItem(
        symbol=symbol,
        name=name,
        description=description,
        units=units
    )


@when(parsers.parse('I try to create a cargo item with {units:d} units'))
def try_create_cargo_item_with_units(context, units):
    """Try to create a cargo item with negative units"""
    try:
        context['cargo_item'] = CargoItem(
            symbol="TEST",
            name="Test Item",
            description="Test",
            units=units
        )
    except ValueError as e:
        context['error'] = e


@when('I try to create a cargo item with empty symbol')
def try_create_cargo_item_empty_symbol(context):
    """Try to create a cargo item with empty symbol"""
    try:
        context['cargo_item'] = CargoItem(
            symbol="",
            name="Test Item",
            description="Test",
            units=10
        )
    except ValueError as e:
        context['error'] = e


@then(parsers.parse('the cargo item should have symbol "{symbol}"'))
def check_cargo_item_symbol(context, symbol):
    """Check cargo item symbol"""
    assert context['cargo_item'].symbol == symbol


@then(parsers.parse('the cargo item should have {units:d} units'))
def check_cargo_item_units(context, units):
    """Check cargo item units"""
    assert context['cargo_item'].units == units


# Cargo creation tests

@given(parsers.parse('a cargo item "{symbol}" with {units:d} units'))
def given_single_cargo_item(context, symbol, units):
    """Create a single cargo item"""
    if 'cargo_items' not in context:
        context['cargo_items'] = []
    context['cargo_items'].append(CargoItem(
        symbol=symbol,
        name=symbol.replace('_', ' ').title(),
        description=f"{symbol} description",
        units=units
    ))


@when('I create cargo with capacity 40 and the items')
def create_cargo_with_items(context):
    """Create cargo with items"""
    items = context['cargo_items']
    total_units = sum(item.units for item in items)
    context['cargo'] = Cargo(
        capacity=40,
        units=total_units,
        inventory=tuple(items)
    )


@when('I try to create cargo with capacity 40 and the item')
def try_create_cargo_exceeding_capacity(context):
    """Try to create cargo exceeding capacity"""
    try:
        items = context['cargo_items']
        total_units = sum(item.units for item in items)
        context['cargo'] = Cargo(
            capacity=40,
            units=total_units,
            inventory=tuple(items)
        )
    except ValueError as e:
        context['error'] = e


@when('I try to create cargo with total units 15 but inventory sums to 10')
def try_create_cargo_mismatched_sum(context):
    """Try to create cargo with mismatched sum"""
    try:
        items = context['cargo_items']
        # Force total_units to 15 even though inventory sums to 10
        context['cargo'] = Cargo(
            capacity=40,
            units=15,
            inventory=tuple(items)
        )
    except ValueError as e:
        context['error'] = e


@then(parsers.parse('the cargo should have capacity {capacity:d}'))
def check_cargo_capacity(context, capacity):
    """Check cargo capacity"""
    assert context['cargo'].capacity == capacity


@then(parsers.parse('the cargo should have {units:d} total units'))
def check_cargo_total_units(context, units):
    """Check cargo total units"""
    assert context['cargo'].units == units


@then(parsers.parse('the cargo should have {count:d} items in inventory'))
def check_cargo_inventory_count(context, count):
    """Check cargo inventory count"""
    assert len(context['cargo'].inventory) == count


# Cargo validation tests

@then(parsers.parse('a ValueError should be raised with message "{message}"'))
def check_value_error(context, message):
    """Check that ValueError was raised with expected message"""
    assert 'error' in context
    assert isinstance(context['error'], ValueError)
    assert str(context['error']) == message


# Cargo has_item tests

@given(parsers.parse('a cargo with IRON_ORE {units:d} units and capacity {capacity:d}'))
def given_cargo_with_iron_ore(context, units, capacity):
    """Create cargo with IRON_ORE"""
    item = CargoItem(symbol="IRON_ORE", name="Iron Ore", description="Raw iron ore", units=units)
    context['cargo'] = Cargo(
        capacity=capacity,
        units=units,
        inventory=(item,)
    )


@given(parsers.parse('a cargo with IRON_ORE {iron_units:d} units and COPPER {copper_units:d} units and capacity {capacity:d}'))
def given_cargo_with_multiple_items(context, iron_units, copper_units, capacity):
    """Create cargo with multiple items"""
    items = (
        CargoItem(symbol="IRON_ORE", name="Iron Ore", description="Raw iron ore", units=iron_units),
        CargoItem(symbol="COPPER", name="Copper", description="Copper ore", units=copper_units)
    )
    total_units = iron_units + copper_units
    context['cargo'] = Cargo(
        capacity=capacity,
        units=total_units,
        inventory=items
    )


@given(parsers.parse('an empty cargo with capacity {capacity:d}'))
def given_empty_cargo(context, capacity):
    """Create empty cargo"""
    context['cargo'] = Cargo(
        capacity=capacity,
        units=0,
        inventory=()
    )


@when(parsers.parse('I check if cargo has "{symbol}" with at least {min_units:d} units'))
def check_has_item(context, symbol, min_units):
    """Check if cargo has item"""
    context['result'] = context['cargo'].has_item(symbol, min_units)


@when(parsers.parse('I check if cargo has "{symbol}" with at least {min_units:d} unit'))
def check_has_item_singular(context, symbol, min_units):
    """Check if cargo has item (singular)"""
    context['result'] = context['cargo'].has_item(symbol, min_units)


@when(parsers.parse('I get units for "{symbol}"'))
def get_item_units(context, symbol):
    """Get units for item"""
    context['result'] = context['cargo'].get_item_units(symbol)


@when(parsers.parse('I check if cargo has items other than "{symbol}"'))
def check_has_items_other_than(context, symbol):
    """Check if cargo has items other than specified symbol"""
    context['result'] = context['cargo'].has_items_other_than(symbol)


@then('the result should be true')
def check_result_true(context):
    """Check result is true"""
    assert context['result'] is True


@then('the result should be false')
def check_result_false(context):
    """Check result is false"""
    assert context['result'] is False


@then(parsers.parse('the result should be {value:d}'))
def check_result_value(context, value):
    """Check result value"""
    assert context['result'] == value


# Cargo capacity tests

@given(parsers.parse('a cargo with capacity {capacity:d} and {units:d} units used'))
def given_cargo_with_units(context, capacity, units):
    """Create cargo with specific units"""
    # Create dummy items to match units
    if units > 0:
        item = CargoItem(symbol="TEST", name="Test", description="Test item", units=units)
        inventory = (item,)
    else:
        inventory = ()

    context['cargo'] = Cargo(
        capacity=capacity,
        units=units,
        inventory=inventory
    )


@when('I check available capacity')
def check_available_capacity(context):
    """Check available capacity"""
    context['result'] = context['cargo'].available_capacity()


@when('I check if cargo is empty')
def check_is_empty(context):
    """Check if cargo is empty"""
    context['result'] = context['cargo'].is_empty()
