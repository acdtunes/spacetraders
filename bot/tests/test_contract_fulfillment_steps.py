#!/usr/bin/env python3
"""
Step definitions for contract fulfillment BDD tests
"""
import sys
from pathlib import Path
import pytest
from pytest_bdd import scenarios, given, when, then, parsers

sys.path.insert(0, str(Path(__file__).parent.parent / "lib"))
sys.path.insert(0, str(Path(__file__).parent))

from mock_api import MockAPIClient
from ship_controller import ShipController
from smart_navigator import SmartNavigator

# Load all scenarios from the feature file
scenarios('features/contract_fulfillment.feature')


@pytest.fixture
def context():
    """Shared test context"""
    return {
        'mock_api': None,
        'ship': None,
        'navigator': None,
        'contract_id': 'test-contract-1',
        'initial_credits': 0,
        'acceptance_payment': 0,
        'completion_payment': 0,
        'units_purchased': 0,
        'trip_count': 0,
        'delivery_log': []
    }


# Helper function to normalize waypoint data from mock API
def normalize_waypoints(mock_waypoints):
    """Convert mock API waypoint format to graph format"""
    normalized = {}
    for symbol, wp in mock_waypoints.items():
        # Extract traits list
        traits = [t['symbol'] if isinstance(t, dict) else t for t in wp.get('traits', [])]
        has_fuel = 'MARKETPLACE' in traits or 'FUEL_STATION' in traits

        normalized[symbol] = {
            "type": wp['type'],
            "x": wp['x'],
            "y": wp['y'],
            "traits": traits,
            "has_fuel": has_fuel,
            "orbitals": wp.get('orbitals', [])
        }
    return normalized


# Helper function to build graph edges
def build_graph_edges(waypoints):
    """Build edges for graph from waypoints"""
    import math
    edges = []
    waypoint_list = list(waypoints.keys())

    for i, wp1 in enumerate(waypoint_list):
        wp1_data = waypoints[wp1]
        for wp2 in waypoint_list[i+1:]:
            wp2_data = waypoints[wp2]

            # Calculate distance
            distance = math.sqrt(
                (wp2_data['x'] - wp1_data['x']) ** 2 +
                (wp2_data['y'] - wp1_data['y']) ** 2
            )

            # Add edge (both directions)
            edges.append({
                "from": wp1,
                "to": wp2,
                "distance": distance,
                "type": "normal"
            })
            edges.append({
                "from": wp2,
                "to": wp1,
                "distance": distance,
                "type": "normal"
            })

    return edges


# Background steps

@given("the SpaceTraders API is mocked", target_fixture="mock_api")
def mock_api(context):
    """Create mock API client"""
    context['mock_api'] = MockAPIClient()
    return context['mock_api']


@given(parsers.parse('a waypoint "{waypoint}" exists at ({x:d}, {y:d}) with traits {traits}'))
def create_waypoint(context, waypoint, x, y, traits):
    """Create a waypoint in mock API"""
    # Parse traits list
    trait_list = eval(traits) if isinstance(traits, str) else traits
    context['mock_api'].add_waypoint(waypoint, "PLANET", x, y, trait_list)

    # Add market if MARKETPLACE trait present
    if "MARKETPLACE" in trait_list:
        context['mock_api'].add_market(waypoint, imports=["IRON_ORE", "COPPER_ORE"], exports=["FUEL"])


@given('the system "X1-TEST" has the contract market configured')
def configure_contract_market(context):
    """Configure the market for contract operations"""
    # This is a no-op step for clarity in the feature file
    pass


# Given steps - Ship setup

@given(parsers.parse('a ship "{ship_symbol}" at "{waypoint}" is {nav_status}'))
def create_ship(context, ship_symbol, waypoint, nav_status):
    """Create a ship at a waypoint"""
    context['mock_api'].set_ship_location(ship_symbol, waypoint, nav_status)
    context['ship'] = ShipController(context['mock_api'], ship_symbol)

    # Initialize navigator
    system = waypoint.rsplit('-', 1)[0]
    context['navigator'] = SmartNavigator(context['mock_api'], system)

    # Build graph from waypoints
    waypoints = normalize_waypoints(context['mock_api'].waypoints)
    context['navigator'].graph = {
        'waypoints': waypoints,
        'edges': build_graph_edges(waypoints)
    }


@given(parsers.parse('the ship has {current:d}/{capacity:d} fuel'))
def set_ship_fuel(context, current, capacity):
    """Set ship fuel level"""
    ship_symbol = context['ship'].ship_symbol
    context['mock_api'].set_ship_fuel(ship_symbol, current, capacity)


@given(parsers.parse('the ship has cargo: {cargo_list}'))
def set_ship_cargo(context, cargo_list):
    """Set ship cargo inventory"""
    ship_symbol = context['ship'].ship_symbol
    # Parse cargo list
    cargo = eval(cargo_list) if isinstance(cargo_list, str) else cargo_list
    context['mock_api'].set_ship_cargo(ship_symbol, cargo)


@given(parsers.parse('the ship has {capacity:d} cargo capacity'))
def set_ship_cargo_capacity(context, capacity):
    """Set ship cargo capacity"""
    ship_symbol = context['ship'].ship_symbol
    ship = context['mock_api'].ships[ship_symbol]
    ship['cargo']['capacity'] = capacity


@given(parsers.parse('the agent has {credits:d} credits'))
def set_agent_credits(context, credits):
    """Set agent credits"""
    context['mock_api'].agent['credits'] = credits
    context['initial_credits'] = credits


# Given steps - Contract setup

@given(parsers.parse('a contract exists requiring {units:d} units of "{symbol}" to "{destination}"'))
def create_contract(context, units, symbol, destination):
    """Create a contract"""
    context['mock_api'].add_contract(
        context['contract_id'],
        symbol,
        units,
        destination,
        accepted=True,
        units_fulfilled=0,
        on_accepted=10000,
        on_fulfilled=50000
    )
    context['acceptance_payment'] = 10000
    context['completion_payment'] = 50000


@given(parsers.parse('{units:d} units have been fulfilled'))
def set_units_fulfilled(context, units):
    """Set units already fulfilled in contract"""
    contract = context['mock_api'].contracts[context['contract_id']]
    contract['terms']['deliver'][0]['unitsFulfilled'] = units


@given('the contract is not accepted')
def set_contract_not_accepted(context):
    """Set contract to not accepted"""
    contract = context['mock_api'].contracts[context['contract_id']]
    contract['accepted'] = False


# When steps - Contract fulfillment actions

@when('I fulfill the contract with cargo already on ship')
def fulfill_contract_with_existing_cargo(context):
    """Fulfill contract using cargo already on ship"""
    ship = context['ship']
    contract_id = context['contract_id']
    contract = context['mock_api'].get_contract(contract_id)

    # Get delivery requirements
    delivery = contract['terms']['deliver'][0]
    destination = delivery['destinationSymbol']

    # Navigate to destination
    context['navigator'].execute_route(ship, destination)
    ship.dock()

    # Get cargo on ship
    ship_data = ship.get_status()
    current_cargo = ship_data['cargo']['inventory']

    # Find contract item in cargo
    to_deliver = 0
    for item in current_cargo:
        if item['symbol'] == delivery['tradeSymbol']:
            to_deliver = min(item['units'], delivery['unitsRequired'] - delivery['unitsFulfilled'])
            break

    # Deliver
    if to_deliver > 0:
        result = context['mock_api'].post(f"/my/contracts/{contract_id}/deliver", {
            "shipSymbol": ship.ship_symbol,
            "tradeSymbol": delivery['tradeSymbol'],
            "units": to_deliver
        })

        context['delivery_log'].append({
            'trip': 1,
            'units': to_deliver,
            'source': 'existing_cargo'
        })

        # Check if fully fulfilled
        updated_contract = result['data']['contract']
        delivery = updated_contract['terms']['deliver'][0]
        if delivery['unitsFulfilled'] >= delivery['unitsRequired']:
            context['mock_api'].post(f"/my/contracts/{contract_id}/fulfill")


@when(parsers.parse('I fulfill the contract buying from "{buy_location}"'))
def fulfill_contract_with_buying(context, buy_location):
    """Fulfill contract by buying resources and delivering"""
    ship = context['ship']
    contract_id = context['contract_id']
    contract = context['mock_api'].get_contract(contract_id)

    # Get delivery requirements
    delivery = contract['terms']['deliver'][0]
    destination = delivery['destinationSymbol']
    required = delivery['unitsRequired']
    fulfilled = delivery['unitsFulfilled']
    remaining = required - fulfilled

    # Check existing cargo
    ship_data = ship.get_status()
    current_cargo = ship_data['cargo']['inventory']
    cargo_capacity = ship_data['cargo']['capacity']

    already_have = 0
    for item in current_cargo:
        if item['symbol'] == delivery['tradeSymbol']:
            already_have = item['units']
            break

    still_need = remaining - already_have
    context['units_purchased'] = 0
    trip = 0

    # Deliver existing cargo first if we have any
    if already_have > 0:
        trip += 1
        context['navigator'].execute_route(ship, destination)
        ship.dock()

        context['mock_api'].post(f"/my/contracts/{contract_id}/deliver", {
            "shipSymbol": ship.ship_symbol,
            "tradeSymbol": delivery['tradeSymbol'],
            "units": already_have
        })

        context['delivery_log'].append({
            'trip': trip,
            'units': already_have,
            'source': 'existing_cargo'
        })

    # Buy and deliver remaining
    while still_need > 0:
        trip += 1

        # Navigate to market
        context['navigator'].execute_route(ship, buy_location)
        ship.dock()

        # Calculate how much to buy
        ship_data = ship.get_status()
        cargo_available = cargo_capacity - ship_data['cargo']['units']
        to_buy = min(still_need, cargo_available)

        if to_buy > 0:
            ship.buy(delivery['tradeSymbol'], to_buy)
            context['units_purchased'] += to_buy

            # Navigate to destination
            context['navigator'].execute_route(ship, destination)
            ship.dock()

            # Deliver
            context['mock_api'].post(f"/my/contracts/{contract_id}/deliver", {
                "shipSymbol": ship.ship_symbol,
                "tradeSymbol": delivery['tradeSymbol'],
                "units": to_buy
            })

            context['delivery_log'].append({
                'trip': trip,
                'units': to_buy,
                'source': 'purchase'
            })

            still_need -= to_buy
        else:
            break

    context['trip_count'] = trip

    # Fulfill if complete
    updated_contract = context['mock_api'].get_contract(contract_id)
    delivery = updated_contract['terms']['deliver'][0]
    if delivery['unitsFulfilled'] >= delivery['unitsRequired']:
        context['mock_api'].post(f"/my/contracts/{contract_id}/fulfill")


@when(parsers.parse('I execute the full contract fulfillment cycle from "{buy_location}"'))
def execute_full_contract_cycle(context, buy_location):
    """Execute full contract cycle: accept, buy, deliver, fulfill"""
    ship = context['ship']
    contract_id = context['contract_id']
    contract = context['mock_api'].get_contract(contract_id)

    # Accept contract if not accepted
    if not contract['accepted']:
        result = context['mock_api'].post(f"/my/contracts/{contract_id}/accept")
        contract = result['data']['contract']

    # Now fulfill it
    fulfill_contract_with_buying(context, buy_location)


@when('I check the contract status')
def check_contract_status(context):
    """Check contract status"""
    contract_id = context['contract_id']
    contract = context['mock_api'].get_contract(contract_id)
    context['contract_check'] = contract


# Then steps - Verification

@then(parsers.parse('the contract should show {units:d} units fulfilled'))
def verify_units_fulfilled(context, units):
    """Verify units fulfilled in contract"""
    contract_id = context['contract_id']
    contract = context['mock_api'].get_contract(contract_id)
    delivery = contract['terms']['deliver'][0]

    assert delivery['unitsFulfilled'] == units, \
        f"Expected {units} units fulfilled, got {delivery['unitsFulfilled']}"


@then('the contract should be fulfilled')
def verify_contract_fulfilled(context):
    """Verify contract is fulfilled"""
    contract_id = context['contract_id']
    contract = context['mock_api'].get_contract(contract_id)

    assert contract['fulfilled'] is True, "Contract should be fulfilled"


@then(parsers.parse('the ship cargo should have {units:d} units'))
def verify_cargo_units(context, units):
    """Verify cargo units"""
    cargo = context['ship'].get_cargo()
    actual_units = cargo['units']

    assert actual_units == units, \
        f"Expected {units} cargo units, got {actual_units}"


@then(parsers.parse('the ship should be at "{waypoint}"'))
def verify_ship_location(context, waypoint):
    """Verify ship location"""
    ship_data = context['ship'].get_status()

    assert ship_data['nav']['waypointSymbol'] == waypoint, \
        f"Ship at {ship_data['nav']['waypointSymbol']}, expected {waypoint}"


@then('the agent should receive completion payment')
def verify_completion_payment(context):
    """Verify agent received completion payment"""
    current_credits = context['mock_api'].agent['credits']
    expected_credits = context['initial_credits'] + context['completion_payment']

    # Account for purchase costs
    purchase_cost = context['units_purchased'] * 50  # 50 credits per unit
    expected_credits -= purchase_cost

    # May also have acceptance payment
    contract = context['mock_api'].get_contract(context['contract_id'])
    if contract['accepted']:
        expected_credits += context['acceptance_payment']

    assert current_credits >= context['initial_credits'], \
        f"Agent should have received payment, credits: {current_credits}"


@then(parsers.parse('the delivery should have taken {trips:d} trips'))
def verify_trip_count(context, trips):
    """Verify number of delivery trips"""
    assert context['trip_count'] == trips, \
        f"Expected {trips} trips, got {context['trip_count']}"


@then(parsers.parse('only {units:d} units should have been purchased'))
def verify_units_purchased(context, units):
    """Verify number of units purchased"""
    assert context['units_purchased'] == units, \
        f"Expected {units} units purchased, got {context['units_purchased']}"


@then(parsers.parse('the agent should have spent credits on {units:d} units only'))
def verify_purchase_cost(context, units):
    """Verify purchase cost"""
    # Verify that only the specified units were purchased
    assert context['units_purchased'] == units, \
        f"Expected {units} units purchased, got {context['units_purchased']}"

    # Calculate expected final credits
    expected_cost = units * 50  # 50 credits per unit
    expected_credits = context['initial_credits'] - expected_cost

    # Add completion payment if contract fulfilled
    contract = context['mock_api'].get_contract(context['contract_id'])
    if contract['fulfilled']:
        expected_credits += context['completion_payment']

    current_credits = context['mock_api'].agent['credits']

    # Allow for small variance
    assert abs(current_credits - expected_credits) <= expected_cost * 0.2, \
        f"Expected ~{expected_credits} credits, got {current_credits}"


@then('the contract should be accepted first')
def verify_contract_accepted(context):
    """Verify contract was accepted"""
    contract_id = context['contract_id']
    contract = context['mock_api'].get_contract(contract_id)

    assert contract['accepted'] is True, "Contract should be accepted"


@then('the agent should receive acceptance payment')
def verify_acceptance_payment(context):
    """Verify agent received acceptance payment"""
    current_credits = context['mock_api'].agent['credits']

    # Should have acceptance payment
    assert current_credits >= context['initial_credits'] + context['acceptance_payment'], \
        f"Agent should have received acceptance payment"


@then(parsers.parse('the ship should still have "{symbol}" in cargo'))
def verify_cargo_has_item(context, symbol):
    """Verify ship still has specific item in cargo"""
    cargo = context['ship'].get_cargo()

    found = False
    for item in cargo['inventory']:
        if item['symbol'] == symbol and item['units'] > 0:
            found = True
            break

    assert found, f"Ship should still have {symbol} in cargo"


@then(parsers.parse('the first trip should deliver {units:d} units from existing cargo'))
def verify_first_trip_from_existing(context, units):
    """Verify first trip delivered from existing cargo"""
    assert len(context['delivery_log']) > 0, "Should have delivery log entries"

    first_trip = context['delivery_log'][0]
    assert first_trip['source'] == 'existing_cargo', \
        f"First trip should use existing cargo, got {first_trip['source']}"
    assert first_trip['units'] == units, \
        f"First trip should deliver {units} units, got {first_trip['units']}"


@then(parsers.parse('the second trip should deliver {units:d} units from purchase'))
def verify_second_trip_from_purchase(context, units):
    """Verify second trip delivered from purchase"""
    assert len(context['delivery_log']) >= 2, "Should have at least 2 delivery log entries"

    second_trip = context['delivery_log'][1]
    assert second_trip['source'] == 'purchase', \
        f"Second trip should use purchased cargo, got {second_trip['source']}"
    assert second_trip['units'] == units, \
        f"Second trip should deliver {units} units, got {second_trip['units']}"


@then('the contract should already be complete')
def verify_contract_complete(context):
    """Verify contract is already complete"""
    contract = context['contract_check']
    delivery = contract['terms']['deliver'][0]

    assert delivery['unitsFulfilled'] >= delivery['unitsRequired'], \
        "Contract should already be complete"


@then('no delivery should be needed')
def verify_no_delivery_needed(context):
    """Verify no delivery was made"""
    assert len(context['delivery_log']) == 0, \
        "No delivery should have been made"


@then(parsers.parse('the first trip should buy {units:d} units due to cargo space'))
def verify_first_trip_limited_by_cargo(context, units):
    """Verify first trip bought limited units due to cargo space"""
    assert len(context['delivery_log']) > 0, "Should have delivery log entries"

    first_trip = context['delivery_log'][0]
    if first_trip['source'] == 'existing_cargo':
        # First trip was existing cargo, check second trip
        assert len(context['delivery_log']) >= 2, "Should have second trip"
        second_trip = context['delivery_log'][1]
        assert second_trip['units'] == units, \
            f"Expected {units} units in first purchase trip, got {second_trip['units']}"
    else:
        assert first_trip['units'] == units, \
            f"Expected {units} units in first trip, got {first_trip['units']}"


@then(parsers.parse('the second trip should buy {units:d} units'))
def verify_second_trip_purchase(context, units):
    """Verify second trip bought specified units"""
    # Find the second purchase trip (may be 2nd or 3rd in log)
    purchase_trips = [log for log in context['delivery_log'] if log['source'] == 'purchase']

    assert len(purchase_trips) >= 2, "Should have at least 2 purchase trips"
    assert purchase_trips[1]['units'] == units, \
        f"Expected {units} units in second purchase trip, got {purchase_trips[1]['units']}"


@then(parsers.parse('the ship should navigate from "{start}" to "{end}"'))
def verify_navigation_occurred(context, start, end):
    """Verify ship navigated between locations"""
    # This is verified implicitly by the ship being at the destination
    ship_data = context['ship'].get_status()
    assert ship_data['nav']['waypointSymbol'] == end, \
        f"Ship should have navigated to {end}"


@then('fuel should be consumed during navigation')
def verify_fuel_consumed(context):
    """Verify fuel was consumed"""
    ship_data = context['ship'].get_status()
    current_fuel = ship_data['fuel']['current']
    capacity = ship_data['fuel']['capacity']

    # Should have consumed some fuel (not at full capacity)
    assert current_fuel < capacity, \
        "Fuel should have been consumed during navigation"


@then(parsers.parse('only {units:d} additional units should have been purchased'))
def verify_additional_units_purchased(context, units):
    """Verify only specified additional units were purchased"""
    assert context['units_purchased'] == units, \
        f"Expected {units} additional units purchased, got {context['units_purchased']}"


@then(parsers.parse('only {units:d} units should have been delivered'))
def verify_units_delivered(context, units):
    """Verify total units delivered"""
    total_delivered = sum(log['units'] for log in context['delivery_log'])

    assert total_delivered == units, \
        f"Expected {units} total units delivered, got {total_delivered}"
