from types import SimpleNamespace

import pytest

from spacetraders_bot.operations import fleet


@pytest.fixture(autouse=True)
def stub_logging(monkeypatch):
    monkeypatch.setattr(fleet, 'setup_logging', lambda *args, **kwargs: None)


class DummyShipAPI:
    def __init__(self):
        self._agent_calls = 0

    def get_agent(self):
        # Return slightly different credits on each call so profit calculations run
        base = 1000 + self._agent_calls * 100
        self._agent_calls += 1
        return {
            'symbol': 'AGENT',
            'credits': base,
            'headquarters': 'X1-AAA-A1',
        }

    def list_ships(self):
        return [{'symbol': 'SHIP-ALPHA'}]

    def get_ship(self, symbol):
        return {
            'symbol': symbol,
            'nav': {
                'waypointSymbol': 'X1-AAA-A1',
                'status': 'IN_ORBIT',
                'flightMode': 'CRUISE',
                'route': {
                    'arrival': '3024-01-01T00:00:00Z'
                },
            },
            'fuel': {'current': 40, 'capacity': 80},
            'cargo': {'units': 5, 'capacity': 60},
        }


def regression_status_operation_lists_all_ships(monkeypatch, capsys):
    monkeypatch.setattr(fleet, 'get_api_client', lambda player_id: DummyShipAPI())

    args = SimpleNamespace(player_id=5, ships='', log_level='INFO')

    rc = fleet.status_operation(args)

    assert rc == 0
    output = capsys.readouterr().out
    assert 'SHIP-ALPHA' in output
    assert 'FLEET STATUS' in output


def regression_monitor_operation_single_iteration(monkeypatch, capsys):
    api = DummyShipAPI()
    monkeypatch.setattr(fleet, 'get_api_client', lambda player_id: api)
    monkeypatch.setattr(fleet.time, 'sleep', lambda *_args, **_kwargs: None)

    args = SimpleNamespace(
        player_id=5,
        ships='SHIP-ALPHA',
        interval=1,
        duration=1,
        log_level='INFO',
    )

    rc = fleet.monitor_operation(args)

    assert rc == 0
    output = capsys.readouterr().out
    assert 'MONITORING COMPLETE' in output
    assert 'Total Profit' in output
