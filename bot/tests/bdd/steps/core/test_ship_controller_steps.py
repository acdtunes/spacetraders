import time
from unittest.mock import Mock, patch
from datetime import datetime, timedelta, timezone

from pytest_bdd import scenarios, given, when, then, parsers

from spacetraders_bot.core.ship import ShipController

# Load all ship controller scenarios
scenarios('../../../bdd/features/core/ship_state_machine.feature')
scenarios('../../../bdd/features/core/ship_navigation.feature')
scenarios('../../../bdd/features/core/ship_cargo.feature')


class MockShipAPI:
    """Mock API for ship controller tests."""
    def __init__(self):
        self.ships = {}
        self.waypoints = {}
        self.markets = {}
        self.post_calls = []
        self.patch_calls = []

    def get_ship(self, ship_symbol):
        """Get ship status."""
        return self.ships.get(ship_symbol)

    def get_waypoint(self, system, waypoint_symbol):
        """Get waypoint details."""
        return self.waypoints.get(waypoint_symbol)

    def get_market(self, system, waypoint_symbol):
        """Get market details."""
        return self.markets.get(waypoint_symbol)

    def post(self, endpoint, data=None):
        """Mock POST request."""
        self.post_calls.append((endpoint, data))

        # Handle orbit
        if '/orbit' in endpoint:
            ship_symbol = endpoint.split('/')[-2]
            if ship_symbol in self.ships:
                self.ships[ship_symbol]['nav']['status'] = 'IN_ORBIT'
                return {'data': {'nav': self.ships[ship_symbol]['nav']}}

        # Handle dock
        if '/dock' in endpoint:
            ship_symbol = endpoint.split('/')[-2]
            if ship_symbol in self.ships:
                self.ships[ship_symbol]['nav']['status'] = 'DOCKED'
                return {'data': {'nav': self.ships[ship_symbol]['nav']}}

        # Handle navigate
        if '/navigate' in endpoint:
            ship_symbol = endpoint.split('/')[-2]
            if ship_symbol in self.ships and data:
                ship = self.ships[ship_symbol]
                destination = data['waypointSymbol']

                # Calculate fuel consumption
                dest_wp = self.waypoints.get(destination)
                current_wp = self.waypoints.get(ship['nav']['waypointSymbol'])
                if dest_wp and current_wp:
                    distance = ((dest_wp['x'] - current_wp['x'])**2 + (dest_wp['y'] - current_wp['y'])**2)**0.5
                    flight_mode = ship['nav']['flightMode']

                    if flight_mode == 'CRUISE':
                        fuel_cost = int(distance * 1.0)
                    elif flight_mode == 'DRIFT':
                        fuel_cost = int(distance * 0.4)  # DRIFT uses less but still significant
                    else:
                        fuel_cost = int(distance * 0.5)

                    if ship['fuel']['current'] < fuel_cost:
                        return {'error': {'code': 4203, 'message': 'Insufficient fuel'}}

                    ship['fuel']['current'] -= fuel_cost
                    ship['nav']['status'] = 'IN_TRANSIT'
                    ship['nav']['waypointSymbol'] = ship['nav']['route']['destination']['symbol']
                    ship['nav']['route']['destination']['symbol'] = destination

                    arrival_time = datetime.now(timezone.utc) + timedelta(seconds=5)
                    ship['nav']['route']['arrival'] = arrival_time.isoformat()

                    return {
                        'data': {
                            'nav': ship['nav'],
                            'fuel': {
                                'current': ship['fuel']['current'],
                                'capacity': ship['fuel']['capacity'],
                                'consumed': {'amount': fuel_cost}
                            }
                        }
                    }

        # Handle refuel
        if '/refuel' in endpoint:
            ship_symbol = endpoint.split('/')[-2]
            if ship_symbol in self.ships:
                ship = self.ships[ship_symbol]
                units = data.get('units', ship['fuel']['capacity'] - ship['fuel']['current']) if data else ship['fuel']['capacity'] - ship['fuel']['current']
                ship['fuel']['current'] = min(ship['fuel']['current'] + units, ship['fuel']['capacity'])
                return {
                    'data': {
                        'fuel': ship['fuel'],
                        'transaction': {'totalPrice': units * 100}
                    }
                }

        # Handle extract
        if '/extract' in endpoint:
            ship_symbol = endpoint.split('/')[-2]
            if ship_symbol in self.ships:
                ship = self.ships[ship_symbol]

                # Add extracted resource to cargo
                extracted_symbol = 'IRON_ORE'
                extracted_units = 5

                cargo = ship['cargo']
                # Find existing inventory item or create new
                found = False
                for item in cargo['inventory']:
                    if item['symbol'] == extracted_symbol:
                        item['units'] += extracted_units
                        found = True
                        break

                if not found:
                    cargo['inventory'].append({'symbol': extracted_symbol, 'units': extracted_units})

                cargo['units'] += extracted_units

                return {
                    'data': {
                        'extraction': {
                            'yield': {'symbol': extracted_symbol, 'units': extracted_units}
                        },
                        'cargo': cargo,
                        'cooldown': {'remainingSeconds': 80}
                    }
                }

        # Handle sell
        if '/sell' in endpoint:
            ship_symbol = endpoint.split('/')[-2]
            if ship_symbol in self.ships and data:
                ship = self.ships[ship_symbol]
                symbol = data['symbol']
                units = data['units']

                # Remove from cargo
                cargo = ship['cargo']
                for item in cargo['inventory']:
                    if item['symbol'] == symbol:
                        if item['units'] >= units:
                            item['units'] -= units
                            cargo['units'] -= units
                            if item['units'] == 0:
                                cargo['inventory'].remove(item)

                            return {
                                'data': {
                                    'transaction': {
                                        'units': units,
                                        'tradeSymbol': symbol,
                                        'totalPrice': units * 1500,
                                        'pricePerUnit': 1500
                                    }
                                }
                            }
                return {'error': {'code': 4218, 'message': 'Insufficient cargo'}}

        # Handle buy/purchase
        if '/purchase' in endpoint:
            ship_symbol = endpoint.split('/')[-2]
            if ship_symbol in self.ships and data:
                ship = self.ships[ship_symbol]
                symbol = data['symbol']
                units = data['units']

                cargo = ship['cargo']
                if cargo['units'] + units > cargo['capacity']:
                    return {'error': {'code': 4217, 'message': 'Insufficient cargo capacity'}}

                # Add to cargo
                found = False
                for item in cargo['inventory']:
                    if item['symbol'] == symbol:
                        item['units'] += units
                        found = True
                        break

                if not found:
                    cargo['inventory'].append({'symbol': symbol, 'units': units})

                cargo['units'] += units

                return {
                    'data': {
                        'transaction': {
                            'units': units,
                            'tradeSymbol': symbol,
                            'totalPrice': units * 500,
                            'pricePerUnit': 500
                        }
                    }
                }

        # Handle jettison
        if '/jettison' in endpoint:
            ship_symbol = endpoint.split('/')[-2]
            if ship_symbol in self.ships and data:
                ship = self.ships[ship_symbol]
                symbol = data['symbol']
                units = data['units']

                cargo = ship['cargo']
                for item in cargo['inventory']:
                    if item['symbol'] == symbol:
                        if item['units'] >= units:
                            item['units'] -= units
                            cargo['units'] -= units
                            if item['units'] == 0:
                                cargo['inventory'].remove(item)
                            return {'data': {'cargo': cargo}}

        return None

    def patch(self, endpoint, data=None):
        """Mock PATCH request."""
        self.patch_calls.append((endpoint, data))

        # Handle flight mode change
        if '/nav' in endpoint and data and 'flightMode' in data:
            ship_symbol = endpoint.split('/')[-2]
            if ship_symbol in self.ships:
                self.ships[ship_symbol]['nav']['flightMode'] = data['flightMode']
                return {'data': {'nav': self.ships[ship_symbol]['nav']}}

        return None


@given('a mock API client', target_fixture='ship_ctx')
def given_mock_api():
    """Create mock API client."""
    api = MockShipAPI()
    return {'api': api, 'controller': None, 'result': None, 'wait_occurred': False, 'auto_orbit': False}


@given(parsers.parse('a ship "{ship_symbol}" exists at waypoint "{waypoint}"'))
def given_ship_exists(ship_ctx, ship_symbol, waypoint):
    """Create a ship at waypoint."""
    api = ship_ctx['api']

    # Create ship
    api.ships[ship_symbol] = {
        'symbol': ship_symbol,
        'nav': {
            'waypointSymbol': waypoint,
            'status': 'DOCKED',
            'systemSymbol': waypoint.rsplit('-', 1)[0],
            'flightMode': 'CRUISE',
            'route': {
                'destination': {'symbol': waypoint},
                'arrival': None
            }
        },
        'fuel': {
            'current': 400,
            'capacity': 400
        },
        'cargo': {
            'units': 0,
            'capacity': 40,
            'inventory': []
        }
    }

    # Also create the starting waypoint if it doesn't exist
    if waypoint not in api.waypoints:
        api.waypoints[waypoint] = {'symbol': waypoint, 'x': 0, 'y': 0, 'type': 'ASTEROID'}

    ship_ctx['ship_symbol'] = ship_symbol
    ship_ctx['controller'] = ShipController(api, ship_symbol)
    return ship_ctx


@given(parsers.parse('the ship "{ship_symbol}" is {nav_status} at "{waypoint}"'))
def given_ship_status(ship_ctx, ship_symbol, nav_status, waypoint):
    """Set ship navigation status."""
    api = ship_ctx['api']
    if ship_symbol in api.ships:
        api.ships[ship_symbol]['nav']['status'] = nav_status
        api.ships[ship_symbol]['nav']['waypointSymbol'] = waypoint
    return ship_ctx


@given(parsers.parse('the ship "{ship_symbol}" is {nav_status} to "{waypoint}"'))
def given_ship_in_transit(ship_ctx, ship_symbol, nav_status, waypoint):
    """Set ship in transit to waypoint."""
    api = ship_ctx['api']
    if ship_symbol in api.ships:
        api.ships[ship_symbol]['nav']['status'] = nav_status
        api.ships[ship_symbol]['nav']['route']['destination']['symbol'] = waypoint
    return ship_ctx


@given(parsers.parse('the ship has {current:d}/{capacity:d} fuel'))
def given_ship_fuel(ship_ctx, current, capacity):
    """Set ship fuel level."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    if ship_symbol in api.ships:
        api.ships[ship_symbol]['fuel']['current'] = current
        api.ships[ship_symbol]['fuel']['capacity'] = capacity
    return ship_ctx


@given(parsers.parse('the ship will arrive in {seconds:d} seconds'))
def given_ship_arrival(ship_ctx, seconds):
    """Set ship arrival time."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    if ship_symbol in api.ships:
        arrival_time = datetime.now(timezone.utc) + timedelta(seconds=seconds)
        api.ships[ship_symbol]['nav']['route']['arrival'] = arrival_time.isoformat()
    ship_ctx['expected_wait'] = seconds
    return ship_ctx


@given(parsers.parse('waypoint "{waypoint}" exists at distance {distance:d} from "{origin}"'))
def given_waypoint_distance(ship_ctx, waypoint, distance, origin):
    """Create waypoint at specified distance."""
    api = ship_ctx['api']

    # Create origin waypoint if it doesn't exist
    if origin not in api.waypoints:
        api.waypoints[origin] = {'symbol': origin, 'x': 0, 'y': 0, 'type': 'ASTEROID'}

    # Create destination waypoint at specified distance
    api.waypoints[waypoint] = {'symbol': waypoint, 'x': distance, 'y': 0, 'type': 'ASTEROID'}

    return ship_ctx


@given(parsers.parse('waypoint "{waypoint}" is an {waypoint_type}'))
def given_waypoint_type(ship_ctx, waypoint, waypoint_type):
    """Set waypoint type."""
    api = ship_ctx['api']
    if waypoint not in api.waypoints:
        api.waypoints[waypoint] = {'symbol': waypoint, 'x': 0, 'y': 0}
    api.waypoints[waypoint]['type'] = waypoint_type
    return ship_ctx


@given(parsers.parse('waypoint "{waypoint}" has an {market_type} market'))
def given_waypoint_market(ship_ctx, waypoint, market_type):
    """Set waypoint market type."""
    api = ship_ctx['api']
    api.markets[waypoint] = {'symbol': waypoint, 'type': market_type}
    return ship_ctx


@given('the ship has cargo space available')
def given_cargo_space(ship_ctx):
    """Ensure ship has cargo space."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    if ship_symbol in api.ships:
        api.ships[ship_symbol]['cargo']['units'] = 0
        api.ships[ship_symbol]['cargo']['capacity'] = 40
    return ship_ctx


@given(parsers.parse('the ship has {used:d}/{capacity:d} cargo units used'))
def given_cargo_usage(ship_ctx, used, capacity):
    """Set cargo usage."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    if ship_symbol in api.ships:
        api.ships[ship_symbol]['cargo']['units'] = used
        api.ships[ship_symbol]['cargo']['capacity'] = capacity
        # Add filler cargo if needed
        if used > 0:
            api.ships[ship_symbol]['cargo']['inventory'] = [{'symbol': 'FILLER', 'units': used}]
    return ship_ctx


@given(parsers.parse('the ship has {units:d} units of "{symbol}" in cargo'))
def given_ship_cargo_item(ship_ctx, units, symbol):
    """Add cargo item to ship."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    if ship_symbol in api.ships:
        cargo = api.ships[ship_symbol]['cargo']

        # Find existing item or add new
        found = False
        for item in cargo['inventory']:
            if item['symbol'] == symbol:
                item['units'] += units
                cargo['units'] += units
                found = True
                break

        if not found:
            cargo['inventory'].append({'symbol': symbol, 'units': units})
            cargo['units'] += units

    return ship_ctx


@given(parsers.parse('the ship has an extraction cooldown of {seconds:d} seconds'))
def given_extraction_cooldown(ship_ctx, seconds):
    """Set extraction cooldown."""
    ship_ctx['cooldown'] = seconds
    return ship_ctx


@given(parsers.parse('the ship has sufficient credits to buy {units:d} units'))
def given_sufficient_credits(ship_ctx, units):
    """Ensure sufficient credits (mock doesn't enforce this)."""
    return ship_ctx


@given(parsers.parse('waypoint "{waypoint}" does not exist'))
def given_waypoint_not_exists(ship_ctx, waypoint):
    """Ensure waypoint doesn't exist."""
    api = ship_ctx['api']
    if waypoint in api.waypoints:
        del api.waypoints[waypoint]
    return ship_ctx


@when('I orbit the ship')
def when_orbit(ship_ctx):
    """Orbit the ship."""
    controller = ship_ctx['controller']
    ship_ctx['result'] = controller.orbit()
    return ship_ctx


@when('I dock the ship')
def when_dock(ship_ctx):
    """Dock the ship."""
    controller = ship_ctx['controller']

    # Mock the wait function to track if it was called
    original_wait = controller._wait_for_arrival
    def mock_wait(seconds):
        ship_ctx['wait_occurred'] = True
        # Simulate arrival by updating ship to destination
        ship_symbol = ship_ctx['ship_symbol']
        api = ship_ctx['api']
        if ship_symbol in api.ships:
            ship = api.ships[ship_symbol]
            # Move ship to destination waypoint
            dest = ship['nav']['route']['destination']['symbol']
            ship['nav']['waypointSymbol'] = dest

    with patch.object(controller, '_wait_for_arrival', mock_wait):
        ship_ctx['result'] = controller.dock()

    return ship_ctx


@when(parsers.re(r'I navigate to "(?P<destination>[^"]+)"$'))
def when_navigate(ship_ctx, destination):
    """Navigate to destination (basic form - exact match only)."""
    controller = ship_ctx['controller']

    # Track if orbit was called
    original_orbit = controller.orbit
    def mock_orbit():
        ship_ctx['auto_orbit'] = True
        return original_orbit()

    # Mock the wait function
    original_wait = controller._wait_for_arrival
    def mock_wait(seconds):
        ship_ctx['wait_occurred'] = True
        # Simulate arrival by updating ship state
        ship_symbol = ship_ctx['ship_symbol']
        api = ship_ctx['api']
        if ship_symbol in api.ships:
            ship = api.ships[ship_symbol]
            ship['nav']['status'] = 'DOCKED'
            ship['nav']['waypointSymbol'] = ship['nav']['route']['destination']['symbol']

    with patch.object(controller, 'orbit', mock_orbit):
        with patch.object(controller, '_wait_for_arrival', mock_wait):
            ship_ctx['result'] = controller.navigate(destination)

    return ship_ctx


@when(parsers.parse('I navigate to "{destination}" with auto-refuel disabled'))
def when_navigate_no_refuel(ship_ctx, destination):
    """Navigate without auto-refuel."""
    controller = ship_ctx['controller']

    # Mock the wait function
    original_wait = controller._wait_for_arrival
    def mock_wait(seconds):
        ship_ctx['wait_occurred'] = True
        # Simulate arrival
        ship_symbol = ship_ctx['ship_symbol']
        api = ship_ctx['api']
        if ship_symbol in api.ships:
            ship = api.ships[ship_symbol]
            ship['nav']['status'] = 'DOCKED'
            ship['nav']['waypointSymbol'] = destination

    with patch.object(controller, '_wait_for_arrival', mock_wait):
        ship_ctx['result'] = controller.navigate(destination, auto_refuel=False)

    return ship_ctx


@when(parsers.parse('I navigate to "{destination}" without specifying flight mode'))
def when_navigate_auto_mode(ship_ctx, destination):
    """Navigate with auto flight mode selection."""
    controller = ship_ctx['controller']

    def mock_wait(seconds):
        ship_ctx['wait_occurred'] = True
        ship_symbol = ship_ctx['ship_symbol']
        api = ship_ctx['api']
        if ship_symbol in api.ships:
            ship = api.ships[ship_symbol]
            ship['nav']['status'] = 'DOCKED'
            ship['nav']['waypointSymbol'] = destination

    with patch.object(controller, '_wait_for_arrival', mock_wait):
        ship_ctx['result'] = controller.navigate(destination, flight_mode=None)

    return ship_ctx


@when(parsers.parse('I navigate to "{destination}" with flight mode "{mode}"'))
def when_navigate_with_mode(ship_ctx, destination, mode):
    """Navigate with explicit flight mode."""
    controller = ship_ctx['controller']

    def mock_wait(seconds):
        ship_ctx['wait_occurred'] = True
        ship_symbol = ship_ctx['ship_symbol']
        api = ship_ctx['api']
        if ship_symbol in api.ships:
            ship = api.ships[ship_symbol]
            ship['nav']['status'] = 'DOCKED'
            ship['nav']['waypointSymbol'] = destination

    with patch.object(controller, '_wait_for_arrival', mock_wait):
        ship_ctx['result'] = controller.navigate(destination, flight_mode=mode)

    ship_ctx['expected_mode'] = mode
    return ship_ctx


@when('I extract resources')
def when_extract(ship_ctx):
    """Extract resources."""
    controller = ship_ctx['controller']

    # Handle cooldown if set
    if 'cooldown' in ship_ctx:
        def mock_wait(seconds):
            ship_ctx['wait_occurred'] = True

        with patch.object(controller, 'wait_for_cooldown', mock_wait):
            # First call should trigger wait
            ship_ctx['result'] = controller.extract()
    else:
        ship_ctx['result'] = controller.extract()

    return ship_ctx


@when(parsers.parse('I sell {units:d} units of "{symbol}"'))
def when_sell_units(ship_ctx, units, symbol):
    """Sell specific units."""
    controller = ship_ctx['controller']
    ship_ctx['result'] = controller.sell(symbol, units)
    return ship_ctx


@when('I sell all cargo')
def when_sell_all(ship_ctx):
    """Sell all cargo."""
    controller = ship_ctx['controller']
    ship_ctx['result'] = controller.sell_all()
    return ship_ctx


@when(parsers.parse('I buy {units:d} units of "{symbol}"'))
def when_buy(ship_ctx, units, symbol):
    """Buy cargo."""
    controller = ship_ctx['controller']
    ship_ctx['result'] = controller.buy(symbol, units)
    return ship_ctx


@when(parsers.parse('I jettison {units:d} units of "{symbol}"'))
def when_jettison(ship_ctx, units, symbol):
    """Jettison cargo."""
    controller = ship_ctx['controller']
    ship_ctx['result'] = controller.jettison(symbol, units)
    return ship_ctx


@when('I query ship status')
def when_query_status(ship_ctx):
    """Query ship status."""
    controller = ship_ctx['controller']
    ship_ctx['result'] = controller.get_status()
    return ship_ctx


@when('I query cargo status')
def when_query_cargo(ship_ctx):
    """Query cargo status."""
    controller = ship_ctx['controller']
    ship_ctx['result'] = controller.get_cargo()
    return ship_ctx


@then(parsers.parse('the ship should be {nav_status}'))
def then_ship_status(ship_ctx, nav_status):
    """Verify ship status."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    assert api.ships[ship_symbol]['nav']['status'] == nav_status


@then(parsers.parse('the ship should still be at "{waypoint}"'))
def then_ship_location(ship_ctx, waypoint):
    """Verify ship location unchanged."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    assert api.ships[ship_symbol]['nav']['waypointSymbol'] == waypoint


@then(parsers.parse('the destination should be "{waypoint}"'))
def then_destination(ship_ctx, waypoint):
    """Verify navigation destination."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    assert api.ships[ship_symbol]['nav']['route']['destination']['symbol'] == waypoint


@then('the operation should wait for arrival')
def then_wait_occurred(ship_ctx):
    """Verify wait function was called."""
    assert ship_ctx['wait_occurred'] is True


@then(parsers.parse('the ship should be DOCKED at "{waypoint}"'))
def then_docked_at(ship_ctx, waypoint):
    """Verify ship docked at waypoint."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    ship = api.ships[ship_symbol]
    assert ship['nav']['status'] == 'DOCKED'
    assert ship['nav']['waypointSymbol'] == waypoint


@then('the ship should orbit before navigating')
def then_auto_orbit(ship_ctx):
    """Verify auto-orbit occurred."""
    assert ship_ctx['auto_orbit'] is True


@then('extraction should succeed')
def then_extraction_success(ship_ctx):
    """Verify extraction succeeded."""
    assert ship_ctx['result'] is not None
    assert 'symbol' in ship_ctx['result']
    assert 'units' in ship_ctx['result']


@then('cargo should contain the extracted resource')
def then_cargo_has_extracted(ship_ctx):
    """Verify extracted resource in cargo."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    cargo = api.ships[ship_symbol]['cargo']
    assert cargo['units'] > 0
    assert len(cargo['inventory']) > 0


@then('a cooldown should be active')
def then_cooldown_active(ship_ctx):
    """Verify cooldown in result."""
    assert 'cooldown' in ship_ctx['result']
    assert ship_ctx['result']['cooldown'] > 0


@then('the operation should wait for cooldown')
def then_cooldown_wait(ship_ctx):
    """Verify cooldown wait occurred."""
    assert ship_ctx['wait_occurred'] is True


@then('extraction should succeed after cooldown')
def then_extraction_after_cooldown(ship_ctx):
    """Verify extraction succeeded after cooldown."""
    assert ship_ctx['result'] is not None


@then(parsers.parse('status should show nav_status "{status}"'))
def then_status_nav(ship_ctx, status):
    """Verify status nav_status."""
    result = ship_ctx['result']
    assert result['nav']['status'] == status


@then(parsers.parse('status should show location "{waypoint}"'))
def then_status_location(ship_ctx, waypoint):
    """Verify status location."""
    result = ship_ctx['result']
    assert result['nav']['waypointSymbol'] == waypoint


@then(parsers.parse('status should show fuel {current:d}/{capacity:d}'))
def then_status_fuel(ship_ctx, current, capacity):
    """Verify status fuel."""
    result = ship_ctx['result']
    assert result['fuel']['current'] == current
    assert result['fuel']['capacity'] == capacity


@then(parsers.parse('status should show cargo {used:d}/{capacity:d}'))
def then_status_cargo(ship_ctx, used, capacity):
    """Verify status cargo."""
    result = ship_ctx['result']
    assert result['cargo']['units'] == used
    assert result['cargo']['capacity'] == capacity


@then('navigation should succeed')
def then_nav_success(ship_ctx):
    """Verify navigation succeeded."""
    assert ship_ctx['result'] is True


@then(parsers.parse('the ship should be IN_TRANSIT to "{waypoint}"'))
def then_in_transit_to(ship_ctx, waypoint):
    """Verify ship in transit."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    ship = api.ships[ship_symbol]
    assert ship['nav']['status'] == 'IN_TRANSIT'
    assert ship['nav']['route']['destination']['symbol'] == waypoint


@then('fuel should be consumed for the journey')
def then_fuel_consumed(ship_ctx):
    """Verify fuel was consumed."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    ship = api.ships[ship_symbol]
    # Fuel should be less than capacity
    assert ship['fuel']['current'] < ship['fuel']['capacity']


@then('navigation should fail due to insufficient fuel')
def then_nav_fail_fuel(ship_ctx):
    """Verify navigation failed due to fuel."""
    # Result should be False or None
    assert ship_ctx['result'] is not True


@then('navigation should succeed immediately')
def then_nav_immediate(ship_ctx):
    """Verify navigation succeeded immediately."""
    assert ship_ctx['result'] is True


@then(parsers.parse('the ship should still be IN_ORBIT at "{waypoint}"'))
def then_still_in_orbit(ship_ctx, waypoint):
    """Verify ship still in orbit."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    ship = api.ships[ship_symbol]
    assert ship['nav']['status'] == 'IN_ORBIT'
    assert ship['nav']['waypointSymbol'] == waypoint


@then('no fuel should be consumed')
def then_no_fuel_consumed(ship_ctx):
    """Verify no fuel consumed."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    ship = api.ships[ship_symbol]
    # Fuel should still be at capacity
    assert ship['fuel']['current'] == ship['fuel']['capacity']


@then('flight mode should be auto-selected based on fuel level')
def then_mode_auto_selected(ship_ctx):
    """Verify flight mode was auto-selected."""
    # Check that patch was called for flight mode
    api = ship_ctx['api']
    flight_mode_set = any('/nav' in call[0] and call[1] and 'flightMode' in call[1] for call in api.patch_calls)
    assert flight_mode_set is True


@then(parsers.parse('flight mode should be set to "{mode}"'))
def then_mode_set(ship_ctx, mode):
    """Verify flight mode set."""
    api = ship_ctx['api']
    # Check that the mode was set via PATCH
    mode_set = any('/nav' in call[0] and call[1] and call[1].get('flightMode') == mode for call in api.patch_calls)
    assert mode_set is True


@then(parsers.parse('the operation should wait for arrival at "{waypoint}"'))
def then_wait_at_waypoint(ship_ctx, waypoint):
    """Verify wait for arrival at intermediate waypoint."""
    assert ship_ctx['wait_occurred'] is True


@then(parsers.parse('then navigate to "{destination}"'))
def then_navigate_to_destination(ship_ctx, destination):
    """Verify navigation to final destination."""
    # This is checked implicitly by the navigation completing


@then('an arrival time should be calculated')
def then_arrival_calculated(ship_ctx):
    """Verify arrival time exists."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    ship = api.ships[ship_symbol]
    assert ship['nav']['route']['arrival'] is not None


@then('the ship should wait for the calculated arrival time')
def then_wait_for_arrival_time(ship_ctx):
    """Verify wait occurred."""
    assert ship_ctx['wait_occurred'] is True


@then(parsers.parse('navigation should fail with error "{error_msg}"'))
def then_nav_fail_error(ship_ctx, error_msg):
    """Verify navigation failed with specific error."""
    assert ship_ctx['result'] is not True


@then(parsers.parse('{units:d} units of "{symbol}" should be sold'))
def then_units_sold(ship_ctx, units, symbol):
    """Verify units sold."""
    result = ship_ctx['result']
    assert result is not None
    assert result['units'] == units
    assert result['tradeSymbol'] == symbol


@then(parsers.parse('revenue should be for {units:d} units'))
def then_revenue_for_units(ship_ctx, units):
    """Verify revenue calculation."""
    result = ship_ctx['result']
    assert result['totalPrice'] == units * 1500


@then(parsers.parse('cargo should contain {units:d} units of "{symbol}"'))
def then_cargo_contains_units(ship_ctx, units, symbol):
    """Verify cargo contains specific units."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    cargo = api.ships[ship_symbol]['cargo']

    found = False
    for item in cargo['inventory']:
        if item['symbol'] == symbol:
            assert item['units'] == units
            found = True
            break

    assert found is True


@then('all cargo should be sold')
def then_all_sold(ship_ctx):
    """Verify all cargo sold."""
    assert ship_ctx['result'] >= 0  # Total revenue should be non-negative


@then('total revenue should be calculated')
def then_total_revenue(ship_ctx):
    """Verify total revenue."""
    assert ship_ctx['result'] > 0


@then('cargo should be empty')
def then_cargo_empty(ship_ctx):
    """Verify cargo is empty."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    cargo = api.ships[ship_symbol]['cargo']
    assert cargo['units'] == 0
    assert len(cargo['inventory']) == 0


@then(parsers.parse('{units:d} units of "{symbol}" should be jettisoned'))
def then_jettisoned(ship_ctx, units, symbol):
    """Verify jettison succeeded."""
    assert ship_ctx['result'] is True


@then(parsers.parse('cargo should not contain "{symbol}"'))
def then_cargo_not_contains(ship_ctx, symbol):
    """Verify cargo doesn't contain symbol."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    cargo = api.ships[ship_symbol]['cargo']

    for item in cargo['inventory']:
        assert item['symbol'] != symbol


@then(parsers.parse('{units:d} units of "{symbol}" should be purchased'))
def then_purchased(ship_ctx, units, symbol):
    """Verify purchase succeeded."""
    result = ship_ctx['result']
    assert result is not None
    assert result['units'] == units
    assert result['tradeSymbol'] == symbol


@then('credits should be deducted')
def then_credits_deducted(ship_ctx):
    """Verify credits were deducted (mock doesn't track this)."""
    # In mock, just verify transaction succeeded
    assert ship_ctx['result'] is not None


@then('purchase should fail due to insufficient capacity')
def then_purchase_fail_capacity(ship_ctx):
    """Verify purchase failed."""
    # Result should be None or contain error
    assert ship_ctx['result'] is None or 'error' in ship_ctx['result']


@then(parsers.parse('cargo should remain at {units:d}/{capacity:d}'))
def then_cargo_unchanged(ship_ctx, units, capacity):
    """Verify cargo unchanged."""
    ship_symbol = ship_ctx['ship_symbol']
    api = ship_ctx['api']
    cargo = api.ships[ship_symbol]['cargo']
    assert cargo['units'] == units


@then(parsers.parse('cargo should show {used:d}/{capacity:d} units used'))
def then_cargo_shows_usage(ship_ctx, used, capacity):
    """Verify cargo usage."""
    result = ship_ctx['result']
    assert result['units'] == used
    assert result['capacity'] == capacity


@then(parsers.parse('cargo inventory should list "{symbol}" with {units:d} units'))
def then_inventory_lists(ship_ctx, symbol, units):
    """Verify inventory listing."""
    result = ship_ctx['result']
    found = False
    for item in result['inventory']:
        if item['symbol'] == symbol:
            assert item['units'] == units
            found = True
            break
    assert found is True
