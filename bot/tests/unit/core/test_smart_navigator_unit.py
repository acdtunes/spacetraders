import json
import tempfile
from pathlib import Path

import pytest

from spacetraders_bot.core.smart_navigator import SmartNavigator


class StubAPI:
    def __init__(self, waypoints):
        self._waypoints = waypoints

    def list_waypoints(self, system_symbol, limit=20, page=1):
        data = [wp for wp in self._waypoints if wp['page'] == page]
        total = len(self._waypoints)
        return {
            'data': [
                {
                    'symbol': row['symbol'],
                    'type': row['type'],
                    'systemSymbol': system_symbol,
                    'x': row['x'],
                    'y': row['y'],
                    'traits': [{'symbol': trait} for trait in row.get('traits', [])],
                    'orbitals': []
                }
                for row in data
            ],
            'meta': {'total': total}
        }


def build_ship_data(integrity=1.0, capacity=400, role='EXPLORER', cooldown=0):
    return {
        'frame': {'integrity': integrity},
        'fuel': {'current': capacity, 'capacity': capacity},
        'registration': {'role': role},
        'cooldown': {'remainingSeconds': cooldown},
    }


def regression_validate_ship_health_branches():
    graph = {
        'system': 'X1-UNIT',
        'waypoints': {'X1-UNIT-A': {'x': 0, 'y': 0, 'has_fuel': True, 'traits': ['MARKETPLACE'], 'orbitals': []}},
        'edges': []
    }
    api = StubAPI([])
    navigator = SmartNavigator(api, 'X1-UNIT', graph=graph)

    # Critical damage
    valid, reason = navigator._validate_ship_health(build_ship_data(integrity=0.3))
    assert not valid and 'Critical damage' in reason

    # No fuel capacity for non-probe roles
    ship_no_fuel = build_ship_data(capacity=0, role='MINER')
    valid, reason = navigator._validate_ship_health(ship_no_fuel)
    assert not valid and 'no fuel capacity' in reason.lower()

    # Warning branch with cooldown - still valid
    ship_warning = build_ship_data(integrity=0.7, cooldown=15)
    valid, reason = navigator._validate_ship_health(ship_warning)
    assert valid and reason == 'Ship health OK'


def regression_ensure_graph_loads_from_json(tmp_path):
    system = 'X1-JSON'
    cache_dir = tmp_path
    graph_path = cache_dir / f'{system}_graph.json'
    graph_data = {
        'system': system,
        'waypoints': {
            f'{system}-A': {'type': 'PLANET', 'x': 0, 'y': 0, 'traits': ['MARKETPLACE'], 'has_fuel': True, 'orbitals': []}
        },
        'edges': []
    }
    graph_path.write_text(json.dumps(graph_data))

    api = StubAPI([])
    navigator = SmartNavigator(api, system, cache_dir=cache_dir, db_path=cache_dir / 'db.sqlite')

    assert navigator.graph['waypoints']


def regression_ensure_graph_builds_when_missing(tmp_path):
    system = 'X1-BUILD'
    api = StubAPI([
        {'symbol': f'{system}-A', 'type': 'PLANET', 'x': 0, 'y': 0, 'traits': ['MARKETPLACE'], 'page': 1},
        {'symbol': f'{system}-B', 'type': 'MOON', 'x': 100, 'y': 0, 'traits': [], 'page': 1},
    ])

    navigator = SmartNavigator(api, system, cache_dir=tmp_path, db_path=tmp_path / 'db.sqlite')

    assert f'{system}-A' in navigator.graph['waypoints']
    assert f'{system}-B' in navigator.graph['waypoints']
