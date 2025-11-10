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
    Mock API client to return ships from context ships_data.

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
    ```
    """
    from unittest.mock import Mock

    def mock_get_api_client(player_id):
        mock_client = Mock()

        def mock_get_ship(ship_symbol):
            # Check if ships_data exists in context
            if 'ships_data' in context and ship_symbol in context['ships_data']:
                return {'data': context['ships_data'][ship_symbol]}
            # Return a default ship if not in context
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

        mock_client.get_ship.side_effect = mock_get_ship
        return mock_client

    monkeypatch.setattr('configuration.container.get_api_client_for_player', mock_get_api_client)
