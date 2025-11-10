import pytest

# Use real repositories from production code - no mocks!
# All repositories backed by in-memory SQLite (configured in root conftest.py)

@pytest.fixture
def player_repo():
    """Get real PlayerRepository (SQLAlchemy + in-memory SQLite)"""
    from configuration.container import get_player_repository
    return get_player_repository()


@pytest.fixture
def ship_repo():
    """Get real ShipRepository (API-only - uses mock_api_client)"""
    from configuration.container import get_ship_repository
    return get_ship_repository()


@pytest.fixture
def context():
    """Provide shared context dictionary for test data"""
    return {}


@pytest.fixture(autouse=True)
def mock_api_client(context, monkeypatch):
    """
    Mock API client to return ships from context ships_data and agent data from context agent_data.

    Usage in tests:
    ```python
    @given('a ship "SHIP-1" exists for player 1')
    def create_ship(context):
        context['ships_data'] = {
            'SHIP-1': {
                'symbol': 'SHIP-1',
                'nav': {'waypointSymbol': 'X1-TEST-A1', 'status': 'DOCKED'},
                'fuel': {'current': 400, 'capacity': 400},
                'cargo': {'capacity': 100, 'units': 0, 'inventory': []},
                'engine': {'speed': 30}
            }
        }

    @given('the API returns agent data')
    def setup_agent(context):
        context['agent_data'] = {
            'data': {
                'symbol': 'TEST-AGENT',
                'credits': 100000,
                'headquarters': 'X1-TEST-A1'
            }
        }
    ```
    """
    from unittest.mock import Mock

    def mock_get_api_client(player_id):
        mock_client = Mock()

        def mock_get_ship(ship_symbol):
            # Check if ships_data exists in context
            if 'ships_data' in context and ship_symbol in context['ships_data']:
                ship_data = context['ships_data'][ship_symbol]

                # Check player ownership - each ship should have player_id metadata
                ship_player_id = ship_data.get('player_id', 1)  # Default to player 1 for backward compatibility
                if ship_player_id != player_id:
                    # Ship belongs to different player - return None (not found)
                    return None

                # Simulate ship arrival for IN_TRANSIT ships
                if ship_data.get('nav', {}).get('status') == 'IN_TRANSIT':
                    from datetime import datetime, timezone
                    # Check if arrival_time is set in context
                    arrival_time = context.get('arrival_time')
                    if arrival_time and datetime.now(timezone.utc) >= arrival_time:
                        # Ship has arrived - transition to IN_ORBIT
                        ship_data['nav']['status'] = 'IN_ORBIT'

                return {'data': ship_data}
            # If ships_data exists but ship is not in it, return None (ship not found)
            if 'ships_data' in context:
                return None
            # If ships_data doesn't exist at all, return a default ship for backwards compatibility
            return {
                'data': {
                    'symbol': ship_symbol,
                    'nav': {
                        'waypointSymbol': 'X1-TEST-A1',
                        'systemSymbol': 'X1-TEST',
                        'status': 'DOCKED',
                        'flightMode': 'CRUISE'
                    },
                    'fuel': {'current': 400, 'capacity': 400},
                    'cargo': {'capacity': 100, 'units': 0, 'inventory': []},
                    'frame': {'symbol': 'FRAME_PROBE'},
                    'reactor': {'symbol': 'REACTOR_SOLAR_I'},
                    'engine': {'symbol': 'ENGINE_IMPULSE_DRIVE_I', 'speed': 30},
                    'modules': [],
                    'mounts': []
                }
            }

        def mock_get_ships():
            # Return all ships from context ships_data
            if 'ships_data' in context:
                return {'data': list(context['ships_data'].values())}
            return {'data': []}

        def mock_get_agent():
            # Return agent data from context if available
            if 'agent_data' in context:
                return context['agent_data']
            # Default agent data for backwards compatibility
            return {
                'data': {
                    'symbol': 'TEST-AGENT',
                    'credits': 100000,
                    'headquarters': 'X1-TEST-A1',
                    'shipCount': 1,
                    'accountId': 'test-123'
                }
            }

        def mock_dock_ship(ship_symbol):
            """Mock dock_ship API call - updates ship status in context"""
            if 'ships_data' in context and ship_symbol in context['ships_data']:
                context['ships_data'][ship_symbol]['nav']['status'] = 'DOCKED'
                return {'data': {'nav': context['ships_data'][ship_symbol]['nav']}}
            return {'data': {'nav': {'status': 'DOCKED'}}}

        def mock_orbit_ship(ship_symbol):
            """Mock orbit_ship API call - updates ship status in context"""
            if 'ships_data' in context and ship_symbol in context['ships_data']:
                context['ships_data'][ship_symbol]['nav']['status'] = 'IN_ORBIT'
                return {'data': {'nav': context['ships_data'][ship_symbol]['nav']}}
            return {'data': {'nav': {'status': 'IN_ORBIT'}}}

        def mock_navigate_ship(ship_symbol, waypoint_symbol):
            """Mock navigate_ship API call - updates ship location and status in context"""
            if 'ships_data' in context and ship_symbol in context['ships_data']:
                ship = context['ships_data'][ship_symbol]
                ship['nav']['waypointSymbol'] = waypoint_symbol
                ship['nav']['status'] = 'IN_TRANSIT'
                # Extract system symbol from waypoint
                parts = waypoint_symbol.split('-')
                system_symbol = f"{parts[0]}-{parts[1]}" if len(parts) >= 2 else 'X1-TEST'
                ship['nav']['systemSymbol'] = system_symbol

                return {
                    'data': {
                        'nav': ship['nav'],
                        'fuel': ship.get('fuel', {'current': 100, 'capacity': 100})
                    }
                }
            return {'data': {'nav': {'status': 'IN_TRANSIT', 'waypointSymbol': waypoint_symbol}}}

        def mock_refuel_ship(ship_symbol, units=None):
            """Mock refuel_ship API call - updates ship fuel in context"""
            if 'ships_data' in context and ship_symbol in context['ships_data']:
                ship = context['ships_data'][ship_symbol]
                fuel_capacity = ship.get('fuel', {}).get('capacity', 100)

                # Refuel to full capacity
                ship['fuel']['current'] = fuel_capacity

                # Calculate units added (for response)
                units_added = fuel_capacity - ship['fuel'].get('current', 0)

                return {
                    'data': {
                        'transaction': {
                            'totalPrice': units_added * 10,  # Mock price
                            'units': units_added
                        },
                        'fuel': ship['fuel']
                    }
                }
            return {'data': {'fuel': {'current': 100, 'capacity': 100}}}

        mock_client.get_ship.side_effect = mock_get_ship
        mock_client.get_ships.side_effect = mock_get_ships
        mock_client.get_agent.side_effect = mock_get_agent
        mock_client.dock_ship.side_effect = mock_dock_ship
        mock_client.orbit_ship.side_effect = mock_orbit_ship
        mock_client.navigate_ship.side_effect = mock_navigate_ship
        mock_client.refuel_ship.side_effect = mock_refuel_ship
        return mock_client

    monkeypatch.setattr('configuration.container.get_api_client_for_player', mock_get_api_client)


@pytest.fixture(autouse=True)
def mock_graph_provider(context, monkeypatch):
    """
    Mock graph provider to avoid API calls for waypoint graph building.

    Returns minimal graph structure with waypoint coordinates.
    """
    from unittest.mock import Mock
    from ports.outbound.graph_provider import GraphLoadResult

    def mock_get_graph_provider(player_id):
        mock_provider = Mock()

        def mock_get_graph(system_symbol, force_refresh=False):
            # Create minimal graph structure from context waypoints
            waypoints = {}

            # Add waypoints from ships_data
            if 'ships_data' in context:
                for ship_data in context['ships_data'].values():
                    nav_data = ship_data.get('nav', {})
                    waypoint_symbol = nav_data.get('waypointSymbol', '')
                    if waypoint_symbol and waypoint_symbol not in waypoints:
                        waypoints[waypoint_symbol] = {
                            'x': 0.0,
                            'y': 0.0,
                            'type': 'PLANET',
                            'traits': []
                        }

            # Add any explicit waypoints from context
            if 'waypoints' in context:
                for wp_symbol, wp_data in context['waypoints'].items():
                    waypoints[wp_symbol] = wp_data

            # Return GraphLoadResult
            graph_dict = {
                'waypoints': waypoints,
                'edges': []
            }

            return GraphLoadResult(graph=graph_dict, source='mock')

        mock_provider.get_graph.side_effect = mock_get_graph
        return mock_provider

    monkeypatch.setattr('configuration.container.get_graph_provider_for_player', mock_get_graph_provider)
