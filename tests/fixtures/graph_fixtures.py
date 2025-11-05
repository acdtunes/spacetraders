"""
Realistic test fixtures matching production data structures.

These fixtures replicate ACTUAL production data structures from:
- System graph database (system_graphs table)
- SpaceTraders API responses

CRITICAL: These structures match production reality. DO NOT fabricate fields.
"""

# ============================================================================
# REAL SYSTEM GRAPH STRUCTURE (from database)
# ============================================================================

# This matches the ACTUAL structure stored in system_graphs.graph_data
# Key observation: waypoint symbol is the DICTIONARY KEY, not a field
REALISTIC_SYSTEM_GRAPH = {
    'system': 'X1-TEST',
    'waypoints': {
        # Waypoint symbol is the KEY (not a field!)
        'X1-TEST-A1': {
            'type': 'PLANET',
            'x': -2,
            'y': 26,
            'traits': ['ROCKY', 'OUTPOST', 'MARKETPLACE'],
            'has_fuel': True,
            'orbitals': ['X1-TEST-A2', 'X1-TEST-A3']
        },
        'X1-TEST-A2': {
            'type': 'MOON',
            'x': -2,
            'y': 26,
            'traits': ['BARREN'],
            'has_fuel': False,
            'orbitals': []
        },
        'X1-TEST-A3': {
            'type': 'MOON',
            'x': -2,
            'y': 26,
            'traits': ['MINERAL_DEPOSITS'],
            'has_fuel': False,
            'orbitals': []
        },
        'X1-TEST-B2': {
            'type': 'PLANET',
            'x': 50,
            'y': 100,
            'traits': ['MARKETPLACE', 'SHIPYARD'],
            'has_fuel': True,
            'orbitals': []
        },
        'X1-TEST-C3': {
            'type': 'FUEL_STATION',
            'x': -50,
            'y': -100,
            'traits': ['MARKETPLACE'],
            'has_fuel': True,
            'orbitals': []
        },
        'X1-TEST-Z9': {
            'type': 'ASTEROID',
            'x': 200,
            'y': 200,
            'traits': ['MINERAL_DEPOSITS', 'COMMON_METAL_DEPOSITS'],
            'has_fuel': False,
            'orbitals': []
        }
    },
    'edges': [
        # Fully connected for test simplicity
        {'from': 'X1-TEST-A1', 'to': 'X1-TEST-B2', 'distance': 92.2, 'type': 'direct'},
        {'from': 'X1-TEST-B2', 'to': 'X1-TEST-A1', 'distance': 92.2, 'type': 'direct'},
        {'from': 'X1-TEST-A1', 'to': 'X1-TEST-C3', 'distance': 134.6, 'type': 'direct'},
        {'from': 'X1-TEST-C3', 'to': 'X1-TEST-A1', 'distance': 134.6, 'type': 'direct'},
        {'from': 'X1-TEST-B2', 'to': 'X1-TEST-C3', 'distance': 223.6, 'type': 'direct'},
        {'from': 'X1-TEST-C3', 'to': 'X1-TEST-B2', 'distance': 223.6, 'type': 'direct'},
        {'from': 'X1-TEST-A1', 'to': 'X1-TEST-Z9', 'distance': 285.6, 'type': 'direct'},
        {'from': 'X1-TEST-Z9', 'to': 'X1-TEST-A1', 'distance': 285.6, 'type': 'direct'},
    ]
}

# Another realistic system for multi-system tests
REALISTIC_SYSTEM_GRAPH_2 = {
    'system': 'X1-ALT',
    'waypoints': {
        'X1-ALT-A1': {
            'type': 'PLANET',
            'x': 0,
            'y': 0,
            'traits': ['MARKETPLACE'],
            'has_fuel': True,
            'orbitals': []
        },
        'X1-ALT-B2': {
            'type': 'ASTEROID',
            'x': 100,
            'y': 0,
            'traits': ['MINERAL_DEPOSITS'],
            'has_fuel': False,
            'orbitals': []
        }
    },
    'edges': [
        {'from': 'X1-ALT-A1', 'to': 'X1-ALT-B2', 'distance': 100.0, 'type': 'direct'},
        {'from': 'X1-ALT-B2', 'to': 'X1-ALT-A1', 'distance': 100.0, 'type': 'direct'},
    ]
}

# System for scout tour tests
REALISTIC_SYSTEM_GRAPH_GZ7 = {
    'system': 'X1-GZ7',
    'waypoints': {
        # Markets for 4-ship fleet test
        'X1-GZ7-A1': {
            'type': 'PLANET',
            'x': 0,
            'y': 0,
            'traits': ['MARKETPLACE'],
            'has_fuel': True,
            'orbitals': []
        },
        'X1-GZ7-A2': {
            'type': 'PLANET',
            'x': 100,
            'y': 0,
            'traits': ['MARKETPLACE'],
            'has_fuel': True,
            'orbitals': []
        },
        'X1-GZ7-A3': {
            'type': 'PLANET',
            'x': 200,
            'y': 0,
            'traits': ['MARKETPLACE'],
            'has_fuel': True,
            'orbitals': []
        },
        'X1-GZ7-A4': {
            'type': 'PLANET',
            'x': 300,
            'y': 0,
            'traits': ['MARKETPLACE'],
            'has_fuel': True,
            'orbitals': []
        },
        'X1-GZ7-B6': {
            'type': 'PLANET',
            'x': 0,
            'y': 100,
            'traits': ['MARKETPLACE'],
            'has_fuel': True,
            'orbitals': []
        },
        'X1-GZ7-B7': {
            'type': 'PLANET',
            'x': 100,
            'y': 100,
            'traits': ['MARKETPLACE'],
            'has_fuel': True,
            'orbitals': []
        },
        'X1-GZ7-C47': {
            'type': 'PLANET',
            'x': 0,
            'y': 200,
            'traits': ['MARKETPLACE'],
            'has_fuel': True,
            'orbitals': []
        },
        'X1-GZ7-C48': {
            'type': 'PLANET',
            'x': 100,
            'y': 200,
            'traits': ['MARKETPLACE'],
            'has_fuel': True,
            'orbitals': []
        },
        'X1-GZ7-D49': {
            'type': 'PLANET',
            'x': 0,
            'y': 300,
            'traits': ['MARKETPLACE'],
            'has_fuel': True,
            'orbitals': []
        },
        'X1-GZ7-D50': {
            'type': 'PLANET',
            'x': 100,
            'y': 300,
            'traits': ['MARKETPLACE'],
            'has_fuel': True,
            'orbitals': []
        },
        'X1-GZ7-E53': {
            'type': 'PLANET',
            'x': 0,
            'y': 400,
            'traits': ['MARKETPLACE'],
            'has_fuel': True,
            'orbitals': []
        },
        'X1-GZ7-E54': {
            'type': 'PLANET',
            'x': 100,
            'y': 400,
            'traits': ['MARKETPLACE'],
            'has_fuel': True,
            'orbitals': []
        },
        # Ship starting location
        'X1-GZ7-H60': {
            'type': 'PLANET',
            'x': 500,
            'y': 0,
            'traits': [],
            'has_fuel': True,
            'orbitals': []
        },
        # Original waypoints for compatibility with existing tests
        'X1-GZ7-B2': {
            'type': 'PLANET',
            'x': 50,
            'y': 50,
            'traits': ['MARKETPLACE'],
            'has_fuel': True,
            'orbitals': []
        },
        'X1-GZ7-C3': {
            'type': 'ASTEROID',
            'x': -50,
            'y': -50,
            'traits': ['MARKETPLACE'],
            'has_fuel': True,
            'orbitals': []
        }
    },
    'edges': [
        # Note: For VRP solver, we don't need explicit edges
        # The routing engine calculates distances directly from waypoint coordinates
    ]
}

# ============================================================================
# REAL API RESPONSE STRUCTURES
# ============================================================================

def create_realistic_ship_response(
    ship_symbol: str = 'TEST-SHIP-1',
    status: str = 'IN_ORBIT',
    waypoint_symbol: str = 'X1-TEST-A1',
    fuel_current: int = 250,
    fuel_capacity: int = 400,
    cargo_units: int = 0,
    cargo_capacity: int = 40,
    engine_speed: int = 30,
    departure_symbol: str = 'X1-TEST-A1',
    destination_symbol: str = 'X1-TEST-B2',
    arrival: str = '2025-10-30T12:00:00Z'
) -> dict:
    """
    Create a realistic API ship response matching SpaceTraders API v2 format.

    This is the ACTUAL structure returned by:
    - GET /my/ships/{shipSymbol}
    - POST /my/ships/{shipSymbol}/navigate
    - POST /my/ships/{shipSymbol}/orbit
    - POST /my/ships/{shipSymbol}/dock
    """
    return {
        'data': {
            'symbol': ship_symbol,
            'nav': {
                'status': status,
                'waypointSymbol': waypoint_symbol,
                'route': {
                    'departure': {'symbol': departure_symbol},
                    'destination': {'symbol': destination_symbol},
                    'arrival': arrival
                }
            },
            'fuel': {
                'current': fuel_current,
                'capacity': fuel_capacity
            },
            'cargo': {
                'units': cargo_units,
                'capacity': cargo_capacity
            },
            'engine': {
                'speed': engine_speed
            }
        }
    }


# ============================================================================
# COMMON TEST SCENARIOS
# ============================================================================

# Ship in orbit ready to navigate
SHIP_IN_ORBIT_RESPONSE = create_realistic_ship_response(
    ship_symbol='TEST-SHIP-1',
    status='IN_ORBIT',
    waypoint_symbol='X1-TEST-A1',
    fuel_current=400,
    fuel_capacity=400
)

# Ship in transit
SHIP_IN_TRANSIT_RESPONSE = create_realistic_ship_response(
    ship_symbol='TEST-SHIP-1',
    status='IN_TRANSIT',
    waypoint_symbol='X1-TEST-A1',  # Current location (departure)
    fuel_current=250,
    fuel_capacity=400,
    departure_symbol='X1-TEST-A1',
    destination_symbol='X1-TEST-B2',
    arrival='2025-10-30T12:00:00Z'
)

# Ship docked at a station
SHIP_DOCKED_RESPONSE = create_realistic_ship_response(
    ship_symbol='TEST-SHIP-1',
    status='DOCKED',
    waypoint_symbol='X1-TEST-A1',
    fuel_current=400,
    fuel_capacity=400
)

# Ship with low fuel
SHIP_LOW_FUEL_RESPONSE = create_realistic_ship_response(
    ship_symbol='TEST-SHIP-1',
    status='IN_ORBIT',
    waypoint_symbol='X1-TEST-A1',
    fuel_current=50,
    fuel_capacity=400
)

# Ship at destination
SHIP_AT_DESTINATION_RESPONSE = create_realistic_ship_response(
    ship_symbol='TEST-SHIP-1',
    status='IN_ORBIT',
    waypoint_symbol='X1-TEST-B2',
    fuel_current=250,
    fuel_capacity=400
)

# ============================================================================
# MOCK GRAPH PROVIDER RESULT
# ============================================================================

class RealisticGraphResult:
    """Mock graph result matching production GraphResult structure"""
    def __init__(self, graph: dict):
        self.graph = graph


def get_mock_graph_for_system(system_symbol: str) -> RealisticGraphResult:
    """Get a realistic mock graph for a system"""
    if system_symbol == 'X1-TEST':
        return RealisticGraphResult(REALISTIC_SYSTEM_GRAPH)
    elif system_symbol == 'X1-ALT':
        return RealisticGraphResult(REALISTIC_SYSTEM_GRAPH_2)
    elif system_symbol == 'X1-GZ7':
        return RealisticGraphResult(REALISTIC_SYSTEM_GRAPH_GZ7)
    else:
        # Minimal fallback for unknown systems
        return RealisticGraphResult({
            'system': system_symbol,
            'waypoints': {}
        })


# ============================================================================
# VALIDATION HELPERS
# ============================================================================

def validate_graph_structure(graph: dict) -> None:
    """
    Validate that a graph has the correct production structure.

    Raises:
        AssertionError: If structure doesn't match production
    """
    assert 'waypoints' in graph, "Graph must have 'waypoints' key"
    waypoints = graph['waypoints']

    for waypoint_symbol, waypoint_data in waypoints.items():
        # CRITICAL: symbol should NOT be a field - it's the dictionary key
        assert 'symbol' not in waypoint_data, (
            f"FAKE MOCK DETECTED: waypoint '{waypoint_symbol}' has 'symbol' field. "
            f"In production, the symbol is the dictionary KEY, not a field!"
        )

        # Required fields
        assert 'type' in waypoint_data, f"Waypoint {waypoint_symbol} missing 'type'"
        assert 'x' in waypoint_data, f"Waypoint {waypoint_symbol} missing 'x'"
        assert 'y' in waypoint_data, f"Waypoint {waypoint_symbol} missing 'y'"
        assert 'traits' in waypoint_data, f"Waypoint {waypoint_symbol} missing 'traits'"
        assert 'has_fuel' in waypoint_data, f"Waypoint {waypoint_symbol} missing 'has_fuel'"
        assert 'orbitals' in waypoint_data, f"Waypoint {waypoint_symbol} missing 'orbitals'"

        # Type validation
        assert isinstance(waypoint_data['x'], (int, float))
        assert isinstance(waypoint_data['y'], (int, float))
        assert isinstance(waypoint_data['traits'], list)
        assert isinstance(waypoint_data['has_fuel'], bool)
        assert isinstance(waypoint_data['orbitals'], list)


def validate_ship_response(response: dict) -> None:
    """
    Validate that a ship response matches SpaceTraders API format.

    Raises:
        AssertionError: If structure doesn't match API
    """
    assert 'data' in response, "Response must have 'data' key"
    data = response['data']

    assert 'symbol' in data, "Ship data must have 'symbol'"
    assert 'nav' in data, "Ship data must have 'nav'"
    assert 'fuel' in data, "Ship data must have 'fuel'"
    assert 'cargo' in data, "Ship data must have 'cargo'"
    assert 'engine' in data, "Ship data must have 'engine'"

    # Nav validation
    nav = data['nav']
    assert 'status' in nav, "Nav must have 'status'"
    assert 'waypointSymbol' in nav, "Nav must have 'waypointSymbol'"
    assert 'route' in nav, "Nav must have 'route'"

    # Fuel validation
    fuel = data['fuel']
    assert 'current' in fuel, "Fuel must have 'current'"
    assert 'capacity' in fuel, "Fuel must have 'capacity'"


# Validate our fixtures on import
validate_graph_structure(REALISTIC_SYSTEM_GRAPH)
validate_graph_structure(REALISTIC_SYSTEM_GRAPH_2)
validate_ship_response(SHIP_IN_ORBIT_RESPONSE)
validate_ship_response(SHIP_IN_TRANSIT_RESPONSE)
validate_ship_response(SHIP_DOCKED_RESPONSE)
