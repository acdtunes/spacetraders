from unittest.mock import MagicMock

import pytest
from pytest_bdd import scenarios, given, when, then, parsers

import spacetraders_bot.core.ship_controller as ship_module

scenarios('features/ship_controller_utilities.feature')


@pytest.fixture
def api_stub():
    class APIStub:
        def __init__(self, ship_payload):
            self.ship_payload = ship_payload
            self.post_calls = []
            self.patch_calls = []

        def get_ship(self, ship_symbol):
            return self.ship_payload

        def post(self, path, payload=None):
            self.post_calls.append((path, payload))
            if path.endswith('/refuel'):
                return {
                    'data': {
                        'transaction': {'totalPrice': 500},
                        'fuel': {'current': 60, 'capacity': 100, 'consumed': {'amount': 0}},
                    }
                }
            if path.endswith('/navigate'):
                return {
                    'data': {
                        'nav': {'route': {'arrival': '2025-01-01T00:00:00Z'}},
                        'fuel': {'consumed': {'amount': 1}, 'current': 90, 'capacity': 100},
                    }
                }
            return {'data': {}}

        def patch(self, path, payload=None):
            self.patch_calls.append((path, payload))
            return {'data': {}}

        def get_waypoint(self, system, waypoint):
            return {'symbol': waypoint, 'x': 0, 'y': 0}

    return APIStub


@pytest.fixture
def controller_context(monkeypatch, api_stub):
    return {'monkeypatch': monkeypatch, 'api_stub': api_stub}


@given(parsers.parse('an in-transit ship at waypoint "{destination}"'))
def given_in_transit(controller_context, destination, api_stub):
    payload = {
        'nav': {
            'status': 'IN_TRANSIT',
            'waypointSymbol': 'ORIGIN',
            'route': {
                'destination': {'symbol': destination},
                'arrival': '2025-01-01T00:00:00Z',
            },
        }
    }
    api = api_stub(payload)
    controller = ship_module.ShipController(api, 'SHIP-1')
    controller_context.update({'api': api, 'controller': controller})
    controller_context['monkeypatch'].setattr(ship_module, 'calculate_arrival_wait_time', lambda arrival: 0)
    controller_context['monkeypatch'].setattr(controller, '_wait_for_arrival', lambda seconds: None)


@when('the ship controller docks the ship')
def when_dock(controller_context):
    result = controller_context['controller'].dock()
    controller_context['result'] = result


@then('docking succeeds')
def then_dock_success(controller_context):
    assert controller_context['result'] is True
    assert controller_context['api'].post_calls[0][0].endswith('/dock')


@given('a docked ship with full fuel')
def given_full_fuel(controller_context, api_stub):
    payload = {
        'nav': {'status': 'DOCKED'},
        'fuel': {'current': 100, 'capacity': 100},
    }
    api = api_stub(payload)
    controller_context.update({'api': api, 'controller': ship_module.ShipController(api, 'SHIP-1')})


@when('a refuel request is made')
def when_refuel(controller_context):
    result = controller_context['controller'].refuel()
    controller_context['result'] = result


@then('no refuel API call is issued')
def then_no_refuel(controller_context):
    assert controller_context['result'] is True
    assert controller_context['api'].post_calls == []


@given('an orbiting ship with low fuel')
def given_low_fuel(controller_context, api_stub):
    payload = {
        'nav': {'status': 'IN_ORBIT'},
        'fuel': {'current': 10, 'capacity': 100},
    }
    api = api_stub(payload)
    controller_context.update({'api': api, 'controller': ship_module.ShipController(api, 'SHIP-1')})
    controller_context['monkeypatch'].setattr(controller_context['controller'], 'dock', lambda: True)


@when(parsers.parse('a refuel request is made for {units:d} units'))
def when_refuel_units(controller_context, units):
    result = controller_context['controller'].refuel(units=units)
    controller_context['result'] = result


@then('a refuel API call is issued with 50 units')
def then_refuel_called(controller_context):
    assert controller_context['result'] is True
    assert controller_context['api'].post_calls[-1][0].endswith('/refuel')
    assert controller_context['api'].post_calls[-1][1] == {'units': 50}


@given(parsers.parse('a ship already in transit to "{destination}"'))
def given_transit_same_dest(controller_context, destination, api_stub):
    payload = {
        'nav': {
            'status': 'IN_TRANSIT',
            'waypointSymbol': 'ORIGIN',
            'route': {
                'destination': {'symbol': destination},
                'arrival': '2025-01-01T00:00:00Z',
            },
        },
    }
    api = api_stub(payload)
    controller = ship_module.ShipController(api, 'SHIP-1')
    wait_calls = []
    controller_context['monkeypatch'].setattr(ship_module, 'calculate_arrival_wait_time', lambda *_: 0)
    controller_context['monkeypatch'].setattr(controller, '_wait_for_arrival', lambda seconds: wait_calls.append(seconds))
    controller_context.update({'api': api, 'controller': controller, 'wait_calls': wait_calls})


@when(parsers.parse('navigation is requested to "{destination}"'))
def when_navigation_requested(controller_context, destination):
    result = controller_context['controller'].navigate(destination, auto_refuel=False)
    controller_context['result'] = result


@then('navigation waits for arrival only')
def then_navigation_waits(controller_context):
    assert controller_context['result'] is True
    assert controller_context['wait_calls'] and controller_context['wait_calls'][0] == 3


@given(parsers.parse('a docked ship needing navigation to "{destination}"'))
def given_docked_ship(controller_context, destination, api_stub):
    payload = {
        'nav': {'status': 'DOCKED', 'waypointSymbol': 'START'},
        'fuel': {'current': 10, 'capacity': 100},
        'engine': {'speed': 10},
    }
    api = api_stub(payload)
    controller = ship_module.ShipController(api, 'SHIP-1')
    controller_context.update({'api': api, 'controller': controller, 'destination': destination})
    mp = controller_context['monkeypatch']
    mp.setattr(ship_module, 'parse_waypoint_symbol', lambda symbol: ('SYS', symbol))
    mp.setattr(ship_module, 'calculate_distance', lambda *_: 20)
    mp.setattr(ship_module, 'select_flight_mode', lambda *args, **kwargs: 'CRUISE')
    mp.setattr(ship_module, 'estimate_fuel_cost', lambda *_: 5)
    mp.setattr(ship_module, 'calculate_arrival_wait_time', lambda *_: 0)
    mp.setattr(controller, '_wait_for_arrival', lambda seconds: None)
    mp.setattr(controller, 'refuel', lambda: True)
    mp.setattr(controller, 'orbit', lambda: True)


@when('navigation is requested with auto refuel')
def when_navigation_auto(controller_context):
    result = controller_context['controller'].navigate(controller_context['destination'])
    controller_context['result'] = result


@then('the ship patches the desired flight mode')
def then_patch_flight_mode(controller_context):
    assert controller_context['api'].patch_calls[-1][1] == {'flightMode': 'CRUISE'}


@then('the ship posts a navigate command')
def then_post_navigate(controller_context):
    assert any(call[0].endswith('/navigate') for call in controller_context['api'].post_calls)


@given('a ship controller waiting for cooldown')
def given_cooldown_controller(controller_context, api_stub):
    payload = {
        'nav': {'status': 'DOCKED'},
        'fuel': {'current': 0, 'capacity': 0},
    }
    api = api_stub(payload)
    controller = ship_module.ShipController(api, 'SHIP-1')
    controller_context.update({'controller': controller, 'sleep_calls': []})
    controller_context['monkeypatch'].setattr(ship_module.time, 'sleep', lambda seconds: controller_context['sleep_calls'].append(seconds))


@when('a cooldown of 3 seconds is requested')
def when_cooldown_requested(controller_context):
    controller_context['controller'].wait_for_cooldown(3)


@then('the wait timer sleeps for progress intervals')
def then_cooldown_progress(controller_context):
    assert controller_context['sleep_calls']
