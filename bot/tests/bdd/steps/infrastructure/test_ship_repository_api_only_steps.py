"""Step definitions for API-only ship repository"""
from unittest.mock import Mock
from pytest_bdd import scenarios, given, when, then, parsers

from adapters.secondary.persistence.ship_repository import ShipRepository
from domain.shared.value_objects import Waypoint

scenarios('../../features/infrastructure/ship_repository_api_only.feature')


@given('a mock API client that returns ship data')
def mock_api_client(context):
    """Create a mock API client"""
    context['mock_api_client'] = Mock()
    context['api_calls'] = []  # Track API calls
    return context


@given('a graph provider for waypoint reconstruction')
def mock_graph_provider(context):
    """Create a mock graph provider"""
    mock_provider = Mock()
    # Return a graph with waypoint data
    mock_provider.get_graph.return_value = Mock(
        graph={
            'waypoints': {
                'X1-A1': {
                    'x': 10.0,
                    'y': 20.0,
                    'systemSymbol': 'X1',
                    'type': 'PLANET',
                    'traits': [],
                    'has_fuel': True,
                    'orbitals': []
                },
                'X1-A2': {
                    'x': 30.0,
                    'y': 40.0,
                    'systemSymbol': 'X1',
                    'type': 'MOON',
                    'traits': [],
                    'has_fuel': False,
                    'orbitals': []
                }
            }
        }
    )
    context['mock_graph_provider'] = mock_provider
    return context


@given('an API-only ship repository')
def api_only_ship_repository(context):
    """Create ship repository with API client factory"""
    def api_client_factory(player_id: int):
        return context['mock_api_client']

    context['repository'] = ShipRepository(
        api_client_factory=api_client_factory,
        graph_provider_factory=lambda player_id: context['mock_graph_provider']
    )
    return context


@given(parsers.parse('the mock API returns ship "{ship_symbol}" with location "{location}"'))
def mock_api_returns_ship(context, ship_symbol, location):
    """Configure mock API to return a specific ship"""
    ship_data = {
        'symbol': ship_symbol,
        'nav': {
            'systemSymbol': location.rsplit('-', 1)[0],
            'waypointSymbol': location,
            'status': 'DOCKED'
        },
        'fuel': {
            'current': 100,
            'capacity': 100
        },
        'cargo': {
            'capacity': 40,
            'units': 0,
            'inventory': []
        },
        'engine': {
            'speed': 30
        }
    }

    def mock_get_ship(symbol):
        context['api_calls'].append(('get_ship', symbol))
        if symbol == ship_symbol:
            return {'data': ship_data}
        raise Exception(f"Ship {symbol} not found")

    context['mock_api_client'].get_ship = mock_get_ship


@given(parsers.parse('the mock API returns 404 for ship "{ship_symbol}"'))
def mock_api_returns_404(context, ship_symbol):
    """Configure mock API to return 404 for a ship"""
    def mock_get_ship(symbol):
        context['api_calls'].append(('get_ship', symbol))
        if symbol == ship_symbol:
            return None
        raise Exception(f"Ship {symbol} not found")

    context['mock_api_client'].get_ship = mock_get_ship


@given(parsers.parse('the mock API returns {count:d} ships for player {player_id:d}'))
def mock_api_returns_ships(context, count, player_id):
    """Configure mock API to return multiple ships"""
    ships_data = []
    for i in range(count):
        ships_data.append({
            'symbol': f'SHIP-{i+1}',
            'nav': {
                'systemSymbol': 'X1',
                'waypointSymbol': 'X1-A1',
                'status': 'DOCKED'
            },
            'fuel': {
                'current': 100,
                'capacity': 100
            },
            'cargo': {
                'capacity': 40,
                'units': 0,
                'inventory': []
            },
            'engine': {
                'speed': 30
            }
        })

    def mock_get_ships():
        context['api_calls'].append(('get_ships', None))
        return {'data': ships_data}

    context['mock_api_client'].get_ships = mock_get_ships


@when(parsers.parse('I find ship "{ship_symbol}" for player {player_id:d}'))
def find_ship(context, ship_symbol, player_id):
    """Find ship by symbol"""
    context['found_ship'] = context['repository'].find_by_symbol(ship_symbol, player_id)


@when(parsers.parse('I list all ships for player {player_id:d}'))
def list_all_ships(context, player_id):
    """List all ships for player"""
    context['ships'] = context['repository'].find_all_by_player(player_id)


@then('the ship should be found')
def ship_should_be_found(context):
    """Verify ship was found"""
    assert context.get('found_ship') is not None, "Ship should be found"


@then('the ship should not be found')
def ship_should_not_be_found(context):
    """Verify ship was not found"""
    assert context.get('found_ship') is None, "Ship should not be found"


@then(parsers.parse('the ship symbol should be "{ship_symbol}"'))
def ship_symbol_should_be(context, ship_symbol):
    """Verify ship symbol"""
    assert context['found_ship'].ship_symbol == ship_symbol


@then(parsers.parse('the ship location should be "{location}"'))
def ship_location_should_be(context, location):
    """Verify ship location"""
    assert context['found_ship'].current_location.symbol == location


@then(parsers.parse('the API client should have been called with "{method}"'))
def api_client_should_be_called(context, method):
    """Verify API client was called"""
    assert any(call[0] == method for call in context['api_calls']), \
        f"API client should have been called with {method}. Calls: {context['api_calls']}"


@then(parsers.parse('I should see {count:d} ships'))
def should_see_ships(context, count):
    """Verify ship count"""
    assert len(context.get('ships', [])) == count, \
        f"Expected {count} ships, got {len(context.get('ships', []))}"
