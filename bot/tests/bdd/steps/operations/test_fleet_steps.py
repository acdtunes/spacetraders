from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, patch
import io
import sys

from spacetraders_bot.operations.fleet import status_operation, monitor_operation

scenarios('../../../bdd/features/operations/fleet.feature')


class MockAPIClient:
    """Mock API client for fleet operations."""
    def __init__(self):
        self.ships = {}
        self.agent = {
            'symbol': 'TEST-AGENT',
            'credits': 50000,
            'headquarters': 'X1-AA-HQ'
        }
        self.credit_sequence = []
        self.credit_index = 0

    def get_agent(self):
        """Get agent data."""
        if self.credit_sequence:
            if self.credit_index < len(self.credit_sequence):
                self.agent['credits'] = self.credit_sequence[self.credit_index]
                self.credit_index += 1
        return self.agent

    def list_ships(self):
        """List all ships."""
        return list(self.ships.values())

    def get_ship(self, ship_symbol):
        """Get ship data."""
        return self.ships.get(ship_symbol)

    def add_ship(self, ship_symbol, location='X1-AA-B1', status='DOCKED', fuel_current=100, fuel_capacity=100, cargo_units=0, cargo_capacity=40, flight_mode='CRUISE', arrival=None):
        """Add a ship to the mock."""
        ship_data = {
            'symbol': ship_symbol,
            'nav': {
                'waypointSymbol': location,
                'status': status,
                'flightMode': flight_mode,
                'route': {
                    'arrival': arrival or '2025-01-01T12:00:00Z'
                }
            },
            'fuel': {
                'current': fuel_current,
                'capacity': fuel_capacity
            },
            'cargo': {
                'units': cargo_units,
                'capacity': cargo_capacity
            }
        }
        self.ships[ship_symbol] = ship_data


@given('a fleet management system', target_fixture='fleet_ctx')
def given_fleet_system():
    """Create fleet management system."""
    return {
        'api': None,
        'result_code': None,
        'output': '',
        'args': None,
    }


@given(parsers.re(r'agent has (?P<count>\d+) ships'))
def given_agent_ships(fleet_ctx, count):
    """Agent has N ships."""
    if 'api' not in fleet_ctx or not fleet_ctx['api']:
        fleet_ctx['api'] = MockAPIClient()

    api = fleet_ctx['api']
    for i in range(int(count)):
        api.add_ship(f'SHIP-{i+1}', location=f'X1-AA-B{i+1}')


@given(parsers.re(r'agent has (?P<credits>\d+) credits'))
def given_agent_credits(fleet_ctx, credits):
    """Set agent credits."""
    if 'api' not in fleet_ctx or not fleet_ctx['api']:
        fleet_ctx['api'] = MockAPIClient()

    fleet_ctx['api'].agent['credits'] = int(credits)


@given(parsers.re(r'agent has ship "(?P<ship>[^"]+)" at "(?P<location>[^"]+)"'))
def given_ship_at_location(fleet_ctx, ship, location):
    """Agent has ship at location."""
    if 'api' not in fleet_ctx or not fleet_ctx['api']:
        fleet_ctx['api'] = MockAPIClient()

    fleet_ctx['api'].add_ship(ship, location=location, status='DOCKED')


@given(parsers.re(r'agent has ship "(?P<ship>[^"]+)" in transit to "(?P<destination>[^"]+)"'))
def given_ship_in_transit(fleet_ctx, ship, destination):
    """Ship is in transit."""
    if 'api' not in fleet_ctx or not fleet_ctx['api']:
        fleet_ctx['api'] = MockAPIClient()

    fleet_ctx['api'].add_ship(
        ship,
        location=destination,
        status='IN_TRANSIT',
        arrival='2025-01-01T14:30:00Z'
    )


@given(parsers.re(r'agent has ship "(?P<ship>[^"]+)" docked at "(?P<location>[^"]+)"'))
def given_ship_docked(fleet_ctx, ship, location):
    """Ship is docked."""
    if 'api' not in fleet_ctx or not fleet_ctx['api']:
        fleet_ctx['api'] = MockAPIClient()

    fleet_ctx['api'].add_ship(ship, location=location, status='DOCKED')


@given(parsers.re(r'agent has ship "(?P<ship>[^"]+)" with fuel (?P<current>\d+)/(?P<capacity>\d+)'))
def given_ship_fuel(fleet_ctx, ship, current, capacity):
    """Ship has specific fuel."""
    if 'api' not in fleet_ctx or not fleet_ctx['api']:
        fleet_ctx['api'] = MockAPIClient()

    fleet_ctx['api'].add_ship(
        ship,
        fuel_current=int(current),
        fuel_capacity=int(capacity)
    )


@given(parsers.re(r'ship "(?P<ship>[^"]+)" has cargo (?P<units>\d+)/(?P<capacity>\d+)'))
def given_ship_cargo(fleet_ctx, ship, units, capacity):
    """Ship has specific cargo."""
    api = fleet_ctx['api']
    if ship in api.ships:
        api.ships[ship]['cargo']['units'] = int(units)
        api.ships[ship]['cargo']['capacity'] = int(capacity)


@given(parsers.re(r'ship "(?P<ship>[^"]+)" is unavailable'))
def given_ship_unavailable(fleet_ctx, ship):
    """Ship is unavailable."""
    # Don't add the ship to the mock - API will return None
    fleet_ctx['unavailable_ships'] = fleet_ctx.get('unavailable_ships', [])
    fleet_ctx['unavailable_ships'].append(ship)


@given(parsers.re(r'agent starts with (?P<credits>\d+) credits'))
def given_starting_credits(fleet_ctx, credits):
    """Set starting credits."""
    if 'api' not in fleet_ctx or not fleet_ctx['api']:
        fleet_ctx['api'] = MockAPIClient()

    fleet_ctx['api'].agent['credits'] = int(credits)
    fleet_ctx['api'].credit_sequence = [int(credits)]


@given(parsers.re(r'after first check agent has (?P<credits>\d+) credits'))
def given_first_check_credits(fleet_ctx, credits):
    """Set credits after first check."""
    api = fleet_ctx['api']
    if len(api.credit_sequence) == 1:
        api.credit_sequence.append(int(credits))


@given(parsers.re(r'after second check agent has (?P<credits>\d+) credits'))
def given_second_check_credits(fleet_ctx, credits):
    """Set credits after second check."""
    api = fleet_ctx['api']
    if len(api.credit_sequence) == 2:
        api.credit_sequence.append(int(credits))


@when('I check fleet status')
def when_check_status(fleet_ctx):
    """Check fleet status."""
    args = Mock()
    args.player_id = 1
    args.ships = None
    args.log_level = 'ERROR'

    fleet_ctx['args'] = args
    _run_fleet_operation(fleet_ctx, status_operation, args)


@when(parsers.re(r'I check fleet status for ships "(?P<ships>[^"]+)"'))
def when_check_specific_ships(fleet_ctx, ships):
    """Check status for specific ships."""
    args = Mock()
    args.player_id = 1
    args.ships = ships
    args.log_level = 'ERROR'

    fleet_ctx['args'] = args
    _run_fleet_operation(fleet_ctx, status_operation, args)


@when(parsers.re(r'I monitor fleet for (?P<checks>\d+) checks with (?P<interval>\d+) minute interval'))
def when_monitor_fleet(fleet_ctx, checks, interval):
    """Monitor fleet."""
    args = Mock()
    args.player_id = 1
    args.ships = None
    args.duration = int(checks)
    args.interval = int(interval)
    args.log_level = 'ERROR'

    fleet_ctx['args'] = args
    # Mock sleep to avoid actual delays
    with patch('spacetraders_bot.operations.fleet.time.sleep'):
        _run_fleet_operation(fleet_ctx, monitor_operation, args)


@when(parsers.re(r'I monitor fleet for (?P<checks>\d+) checks? with (?P<interval>\d+) minute interval for ships "(?P<ships>[^"]+)"'))
def when_monitor_specific_ships(fleet_ctx, checks, interval, ships):
    """Monitor specific ships."""
    args = Mock()
    args.player_id = 1
    args.ships = ships
    args.duration = int(checks)
    args.interval = int(interval)
    args.log_level = 'ERROR'

    fleet_ctx['args'] = args
    # Mock sleep to avoid actual delays
    with patch('spacetraders_bot.operations.fleet.time.sleep'):
        _run_fleet_operation(fleet_ctx, monitor_operation, args)


def _run_fleet_operation(fleet_ctx, operation_func, args):
    """Helper to run fleet operation with mocks."""
    # Capture stdout
    captured_output = io.StringIO()
    sys.stdout = captured_output

    api = fleet_ctx['api']

    # Mock the API client
    with patch('spacetraders_bot.operations.fleet.get_api_client', return_value=api):
        result = operation_func(args)

    # Restore stdout
    sys.stdout = sys.__stdout__

    fleet_ctx['output'] = captured_output.getvalue()
    fleet_ctx['result_code'] = result


@then('agent summary should be displayed')
def then_agent_summary_displayed(fleet_ctx):
    """Verify agent summary is shown."""
    output = fleet_ctx['output']
    assert 'AGENT INFO' in output
    assert 'Callsign:' in output
    assert 'Credits:' in output


@then(parsers.re(r'(?P<count>\d+) ships? should be displayed'))
def then_ships_displayed(fleet_ctx, count):
    """Verify ship count displayed."""
    output = fleet_ctx['output']
    expected_count = int(count)

    # Count ship symbols (SHIP-) in output
    ship_count = output.count('SHIP-')
    assert ship_count >= expected_count, f"Expected at least {expected_count} ships, found {ship_count}"


@then(parsers.re(r'ship "(?P<ship>[^"]+)" should be shown'))
def then_ship_shown(fleet_ctx, ship):
    """Verify specific ship is shown."""
    assert ship in fleet_ctx['output']


@then(parsers.re(r'ship "(?P<ship>[^"]+)" should show (?P<status>\w+) status'))
def then_ship_status(fleet_ctx, ship, status):
    """Verify ship status."""
    output = fleet_ctx['output']
    assert ship in output
    assert status in output


@then('ETA should be displayed')
def then_eta_displayed(fleet_ctx):
    """Verify ETA is shown."""
    assert 'ETA:' in fleet_ctx['output']


@then(parsers.re(r'location should show "(?P<location>[^"]+)"'))
def then_location_shown(fleet_ctx, location):
    """Verify location is shown."""
    assert location in fleet_ctx['output']


@then(parsers.re(r'fuel should show "(?P<fuel>[^"]+)"'))
def then_fuel_shown(fleet_ctx, fuel):
    """Verify fuel display."""
    assert f'Fuel: {fuel}' in fleet_ctx['output']


@then(parsers.re(r'cargo should show "(?P<cargo>[^"]+)"'))
def then_cargo_shown(fleet_ctx, cargo):
    """Verify cargo display."""
    assert f'Cargo: {cargo}' in fleet_ctx['output']


@then(parsers.re(r'(?P<count>\d+) status checks should be performed'))
def then_status_checks_performed(fleet_ctx, count):
    """Verify number of status checks."""
    output = fleet_ctx['output']
    check_count = output.count('CHECK #')
    assert check_count == int(count)


@then('profit should be calculated')
def then_profit_calculated(fleet_ctx):
    """Verify profit calculation."""
    output = fleet_ctx['output']
    assert 'Total Profit:' in output or 'profit' in output.lower()


@then(parsers.re(r'final profit should show (?P<profit>\d+) credits'))
def then_final_profit(fleet_ctx, profit):
    """Verify final profit amount."""
    output = fleet_ctx['output']
    assert 'Total Profit:' in output
    # The profit should be somewhere in the output
    assert str(profit) in output or format(int(profit), ',') in output


@then(parsers.re(r'ship "(?P<ship>[^"]+)" should be displayed'))
def then_specific_ship_displayed(fleet_ctx, ship):
    """Verify specific ship is displayed."""
    assert ship in fleet_ctx['output']


@then(parsers.re(r'ship "(?P<ship>[^"]+)" should show unavailable message'))
def then_ship_unavailable_message(fleet_ctx, ship):
    """Verify unavailable message."""
    output = fleet_ctx['output']
    assert ship in output
    assert 'unavailable' in output
