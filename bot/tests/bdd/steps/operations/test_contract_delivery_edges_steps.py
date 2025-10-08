from types import SimpleNamespace

from pytest_bdd import scenarios, given, when, then, parsers

from spacetraders_bot.operations import contracts


# Helper stubs replicated from unit tests
class NullCaptain(list):
    def log_entry(self, entry_type, **kwargs):
        self.append((entry_type, kwargs))


class FakeNavigator:
    def __init__(self):
        self.calls = []

    def execute_route(self, ship, destination, *_, **__):
        self.calls.append(destination)
        ship.location = destination
        return True


class FakeDB:
    def __init__(self, fetch_fn=lambda symbol, pattern: None):
        self.fetch_fn = fetch_fn

    class Conn:
        def __init__(self, outer):
            self.outer = outer

        def __enter__(self):
            return self

        def __exit__(self, exc_type, exc, tb):
            return False

        class Cursor:
            def __init__(self, result):
                self.result = result

            def fetchone(self):
                return self.result

        def execute(self, query, params):
            result = self.outer.fetch_fn(*params)
            return self.Cursor(result)

    def connection(self):
        return self.Conn(self)

    def get_system_graph(self, _conn, _system):
        return None


class CursorStub:
    def __init__(self, row):
        self._row = row

    def fetchone(self):
        return self._row


class ConnStub:
    def __init__(self, mapping):
        self.mapping = mapping

    def __enter__(self):
        return self

    def __exit__(self, exc_type, exc, tb):
        return False

    def execute(self, query, params):
        key = tuple(params)
        if len(params) == 2 and params[1].endswith('%'):
            key = (params[0], params[1])
        return CursorStub(self.mapping.get(key))


class DBHelperStub:
    def __init__(self, mapping):
        self.mapping = mapping

    def connection(self):
        return ConnStub(self.mapping)


class FakeContractAPI:
    def __init__(self, contract, purchasing_ship):
        self.contract = contract
        self.ship = purchasing_ship
        self.market_data = {}
        self.accept_calls = 0
        self.delivered_units = []

    def get_contract(self, contract_id):
        return self.contract

    def post(self, path, payload=None):
        if path.endswith('/accept'):
            self.accept_calls += 1
            self.contract['accepted'] = True
            return {'data': {'contract': self.contract}}
        if path.endswith('/deliver'):
            units = payload['units']
            self.delivered_units.append(units)
            self.ship.cargo_units = max(0, self.ship.cargo_units - units)
            return {'data': {'delivered': units}}
        if path.endswith('/fulfill'):
            return {'data': {'contract': self.contract}}
        return None

    def get_ship(self, ship_symbol):
        return self.ship.get_status()

    def get_market(self, system, waypoint):
        return self.market_data.get(waypoint)


class PurchaseShip:
    def __init__(self, location="X1-TEST-A1", capacity=60):
        self.ship_symbol = 'PURCHASER'
        self.location = location
        self.capacity = capacity
        self.cargo_units = 0
        self._failed_once = False
        self.buy_calls = []
        self.api = SimpleNamespace(request=lambda method, path: {'data': 'ok'})
        self._initial_attempt = True

    def get_status(self):
        return {
            'nav': {'systemSymbol': 'X1-TEST', 'waypointSymbol': self.location},
            'cargo': {
                'inventory': ([{'symbol': 'IRON_ORE', 'units': self.cargo_units}]
                              if self.cargo_units else []),
                'capacity': self.capacity,
                'units': self.cargo_units,
            },
            'fuel': {'current': 100, 'capacity': 100},
        }

    def navigate(self, destination):
        self.location = destination
        return True

    def dock(self):
        return True

    def buy(self, symbol, units):
        self.buy_calls.append(units)
        if self._initial_attempt:
            self._initial_attempt = False
            return {'units': 0, 'totalPrice': 0}
        if units > 20 and not self._failed_once:
            self._failed_once = True
            return None
        self.cargo_units += units
        return {'units': units, 'totalPrice': units * 90, 'cooldown': 0}

    def extract(self):
        return None


class DockFailShip(PurchaseShip):
    def dock(self):
        return False


class ShipMissingStatus(PurchaseShip):
    def get_status(self):
        return None


class LiveShip(PurchaseShip):
    def __init__(self):
        super().__init__(location='X1-TEST-A1', capacity=5)

    def get_status(self):
        return {
            'nav': {'systemSymbol': 'X1-TEST', 'waypointSymbol': self.location},
            'cargo': {
                'capacity': self.capacity,
                'units': self.cargo_units,
                'inventory': ([{'symbol': 'IRON_ORE', 'units': self.cargo_units}]
                              if self.cargo_units else []),
            },
            'fuel': {'capacity': 100, 'current': 90},
            'cooldown': {'remainingSeconds': 0},
        }

    def get_cargo(self):
        return {
            'capacity': self.capacity,
            'units': self.cargo_units,
            'inventory': ([{'symbol': 'IRON_ORE', 'units': self.cargo_units}]
                          if self.cargo_units else []),
        }


class LiveAPI(FakeContractAPI):
    def __init__(self, contract, ship):
        super().__init__(contract, ship)
        self.market_calls = 0

    def get_market(self, system, waypoint):
        self.market_calls += 1
        return {'tradeGoods': [{'symbol': 'IRON_ORE', 'sellPrice': 100, 'tradeVolume': 10}]}

    def get_agent(self):
        return {'credits': 10_000}

    def post(self, path, payload=None):
        if path.endswith('/deliver'):
            units = payload['units']
            self.ship.cargo_units = max(0, self.ship.cargo_units - units)
            self.delivered_units.append(units)
            return {'data': {'delivered': units}}
        if path.endswith('/fulfill'):
            return {'data': {'contract': self.contract}}
        return super().post(path, payload)


class RouteNavigator(FakeNavigator):
    def execute_route(self, ship, destination, prefer_cruise=True, operation_controller=None):
        ship.location = destination
        return True


def patch_contract_helpers(monkeypatch, captain_logger):
    monkeypatch.setattr(contracts, 'setup_logging', lambda *a, **k: 'logfile.log')
    monkeypatch.setattr(contracts, 'get_captain_logger', lambda *_: captain_logger)
    monkeypatch.setattr(
        contracts,
        'log_captain_event',
        lambda logger, entry_type, **kwargs: captain_logger.log_entry(entry_type, **kwargs),
    )


def make_contract(accepted=True):
    return {
        "id": "CONTRACT-1",
        "accepted": accepted,
        "terms": {
            "deliver": [
                {
                    "tradeSymbol": "IRON_ORE",
                    "unitsRequired": 10,
                    "unitsFulfilled": 0,
                    "destinationSymbol": "X1-TEST-B1",
                }
            ],
            "payment": {"onAccepted": 1000, "onFulfilled": 5000},
            "deadline": "2025-10-08T00:00:00Z",
        },
    }


scenarios('../../features/operations/contract_delivery_edges.feature')


@given('a contract delivery context', target_fixture='contract_ctx')
def given_contract_context(monkeypatch):
    captain = NullCaptain()
    patch_contract_helpers(monkeypatch, captain)
    context = {
        'captain': captain,
        'navigator': FakeNavigator(),
        'sleep_calls': [],
    }
    return context


@given('the purchasing ship fails to dock at the shipyard')
def given_dock_failure_context(contract_ctx):
    context = contract_ctx
    ship = DockFailShip()
    api = FakeContractAPI(make_contract(), ship)
    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        contract_id='CONTRACT-1',
        buy_from='X1-TEST-M1',
        log_level='INFO',
    )
    context.update({
        'api': api,
        'ship': ship,
        'args': args,
        'db': FakeDB(fetch_fn=lambda symbol, pattern: ('X1-TEST-M1', 90, 'ABUNDANT')),
        'sleep_fn': lambda *_: None,
    })
    return context


@given('the delivery API fails once with code 4502')
def given_retry_scenario(contract_ctx):
    context = contract_ctx

    class RetryAPI(FakeContractAPI):
        def __init__(self, contract, ship):
            super().__init__(contract, ship)
            self.failures = 0

        def post(self, path, payload=None):
            if path.endswith('/deliver'):
                self.failures += 1
                if self.failures == 1:
                    return {'error': {'code': 4502, 'message': 'Try again'}}
                response = super().post(path, payload)
                return response
            return super().post(path, payload)

    contract = make_contract()
    contract['terms']['deliver'][0]['unitsRequired'] = 5

    ship = PurchaseShip()
    ship._initial_attempt = False
    api = RetryAPI(contract, ship)
    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        contract_id='CONTRACT-1',
        buy_from='X1-TEST-M1',
        log_level='INFO',
    )

    context.update({
        'api': api,
        'ship': ship,
        'args': args,
        'db': FakeDB(fetch_fn=lambda symbol, pattern: ('X1-TEST-M1', 90, 'ABUNDANT')),
        'sleep_fn': lambda seconds: context['sleep_calls'].append(seconds),
    })
    return context


@given(parsers.parse('the ship starts at "{waypoint}" with {units:d} units of cargo'))
def given_ship_with_cargo(contract_ctx, waypoint, units):
    ship = PurchaseShip(location=waypoint)
    ship.cargo_units = units
    contract_ctx['ship'] = ship
    return contract_ctx


@given(parsers.parse('the contract requires {units:d} units of IRON_ORE to "{destination}"'))
def given_contract_requirements(contract_ctx, units, destination):
    ship = contract_ctx.get('ship') or PurchaseShip()
    contract = make_contract()
    contract['terms']['deliver'][0]['unitsRequired'] = units
    contract['terms']['deliver'][0]['destinationSymbol'] = destination
    contract_ctx['ship'] = ship
    contract_ctx['contract'] = contract

    api = FakeContractAPI(contract, ship)
    args = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        contract_id=contract['id'],
        buy_from=None,
        log_level='INFO',
    )

    contract_ctx.update({
        'api': api,
        'args': args,
        'db': contract_ctx.get('db', FakeDB(fetch_fn=lambda symbol, pattern: ('X1-TEST-M1', 90, 'ABUNDANT'))),
        'sleep_fn': contract_ctx.get('sleep_fn', lambda seconds: contract_ctx['sleep_calls'].append(seconds)),
    })
    return contract_ctx


@given('no markets offer IRON_ORE')
def given_no_markets(contract_ctx):
    contract_ctx['db'] = FakeDB(fetch_fn=lambda symbol, pattern: None)
    contract_ctx['sleep_fn'] = lambda seconds: contract_ctx['sleep_calls'].append(seconds)
    if 'ship' not in contract_ctx or contract_ctx['ship'] is None:
        contract_ctx['ship'] = PurchaseShip()
    return contract_ctx


@given('the contract is not accepted and has a transaction limit')
def given_contract_needs_acceptance(contract_ctx):
    contract = contract_ctx['contract']
    contract['accepted'] = False
    contract['terms']['deliver'][0]['unitsRequired'] = 30
    ship = contract_ctx.get('ship') or PurchaseShip()
    contract_ctx['ship'] = ship
    fetch_fn = lambda symbol, pattern: ('X1-TEST-M1', 90, 'ABUNDANT') if pattern.endswith('%') or pattern == 'X1-TEST-M1' else None
    contract_ctx['db'] = FakeDB(fetch_fn=fetch_fn)
    contract_ctx['sleep_fn'] = lambda seconds: contract_ctx['sleep_calls'].append(seconds)
    contract_ctx['api'] = FakeContractAPI(contract, ship)
    contract_ctx['args'] = SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        contract_id=contract['id'],
        buy_from=None,
        log_level='INFO',
    )
    return contract_ctx


@given('the API returns no contract')
def given_api_returns_none(contract_ctx):
    ship = contract_ctx.get('ship') or PurchaseShip()

    class EmptyAPI(FakeContractAPI):
        def get_contract(self, contract_id):
            return None

    contract = contract_ctx.get('contract', make_contract())
    contract_ctx['api'] = EmptyAPI(contract, ship)
    contract_ctx['ship'] = ship
    contract_ctx['sleep_fn'] = contract_ctx.get('sleep_fn', lambda seconds: contract_ctx['sleep_calls'].append(seconds))
    contract_ctx.setdefault('args', SimpleNamespace(
        player_id=1,
        ship='SHIP-1',
        contract_id=contract['id'],
        buy_from=None,
        log_level='INFO',
    ))
    return contract_ctx


@given('the contract is already fulfilled')
def given_contract_already_fulfilled(contract_ctx):
    contract = contract_ctx['contract']
    deliver = contract['terms']['deliver'][0]
    deliver['unitsFulfilled'] = deliver['unitsRequired']
    return contract_ctx


@given('the ship status cannot be retrieved')
def given_ship_missing_status(contract_ctx):
    ship = ShipMissingStatus(location='X1-TEST-A1')
    contract_ctx['ship'] = ship
    contract_ctx['api'] = FakeContractAPI(contract_ctx['contract'], ship)
    contract_ctx['sleep_fn'] = contract_ctx.get('sleep_fn', lambda seconds: contract_ctx['sleep_calls'].append(seconds))
    return contract_ctx


@given('the contract requires multi-step deliveries')
def given_contract_multistep(contract_ctx):
    ship = LiveShip()
    contract = make_contract()
    contract['terms']['deliver'][0]['unitsRequired'] = 6
    api = LiveAPI(contract, ship)
    contract_ctx.update({
        'ship': ship,
        'contract': contract,
        'api': api,
        'navigator': RouteNavigator(),
        'args': SimpleNamespace(player_id=1, ship='SHIP-1', contract_id=contract['id'], buy_from=None, log_level='INFO'),
        'db': DBHelperStub({
            ('IRON_ORE', 'X1-TEST%'): ('X1-TEST-M1', 100, 'ABUNDANT'),
            ('IRON_ORE', 'X1-TEST-M1'): ('X1-TEST-M1', 100, 'ABUNDANT'),
        }),
        'sleep_fn': lambda seconds: contract_ctx['sleep_calls'].append(seconds),
    })
    return contract_ctx


@when('the contract operation runs')
def when_contract_runs(contract_ctx, capsys):
    context = contract_ctx
    api = context['api']
    ship = context['ship']
    navigator = context.get('navigator', context['navigator'])
    db = context.get('db', FakeDB())
    sleep_fn = context.get('sleep_fn', lambda seconds: None)

    result = contracts.contract_operation(
        context['args'],
        api=api,
        ship=ship,
        navigator=navigator,
        db=db,
        sleep_fn=sleep_fn,
    )
    captured = capsys.readouterr()
    context['stdout'] = captured.out
    context['stderr'] = captured.err
    context['result'] = result
    return context


@then('the contract operation should exit with status 1')
def then_contract_failed(contract_ctx):
    assert contract_ctx['result'] == 1


@then('a critical contract error should be logged')
def then_contract_error_logged(contract_ctx):
    assert any(event[0] == 'CRITICAL_ERROR' for event in contract_ctx['captain'])


@then('the contract operation should exit with status 0')
def then_contract_succeeds(contract_ctx):
    assert contract_ctx['result'] == 0


@then('the delivery is eventually recorded')
def then_delivery_recorded(contract_ctx):
    context = contract_ctx
    assert context['api'].delivered_units == [5]
    assert context['sleep_calls'] == [2]
    assert any(event[0] == 'OPERATION_COMPLETED' for event in context['captain'])


@then(parsers.parse('the delivery record should contain "{units}"'))
def then_delivery_record_contains(contract_ctx, units):
    expected = [int(unit) for unit in units.split(',') if unit]
    assert contract_ctx['api'].delivered_units == expected


@then(parsers.parse('the navigator should visit "{waypoint}"'))
def then_navigator_visit(contract_ctx, waypoint):
    assert waypoint in contract_ctx['navigator'].calls


@then(parsers.parse('the captain log should include "{entry}"'))
def then_captain_log_contains(contract_ctx, entry):
    assert any(event[0] == entry for event in contract_ctx['captain'])


@then(parsers.parse('the sleep function should be called {count:d} times'))
def then_sleep_calls(contract_ctx, count):
    assert len(contract_ctx['sleep_calls']) == count


@then('the contract should be accepted')
def then_contract_accepted(contract_ctx):
    assert contract_ctx['api'].contract['accepted'] is True
    assert contract_ctx['api'].accept_calls >= 1


@then(parsers.parse('the ship should plan purchases from "{market}"'))
def then_buy_from_updated(contract_ctx, market):
    assert contract_ctx['args'].buy_from == market


@then('the first purchase attempt should exceed 20 units')
def then_purchase_exceeds_limit(contract_ctx):
    ship = contract_ctx['ship']
    assert ship.buy_calls
    assert ship.buy_calls[0] > 20


@then(parsers.parse('stdout should contain "{text}"'))
def then_stdout_contains(contract_ctx, text):
    assert text in contract_ctx.get('stdout', '')


@given('a market database with primary and fallback listings', target_fixture='market_ctx')
def given_market_database():
    mapping = {
        ('IRON', 'X1-M1'): ('X1-M1', 100, 'ABUNDANT'),
        ('IRON', 'X1-%'): ('X1-M1', 120, 'COMMON'),
    }
    return {'db': DBHelperStub(mapping)}


@when(parsers.parse('I search for market listings for "{symbol}"'), target_fixture='market_ctx')
def when_search_market_listings(market_ctx, symbol):
    db = market_ctx['db']
    market_ctx['exact'] = contracts._fetch_market_listing(db, symbol, 'X1-M1')
    market_ctx['fallback'] = contracts._find_lowest_price_market(db, symbol, 'X1-')
    return market_ctx


@then(parsers.parse('the exact listing should be "{symbol},{price:d},{abundance}"'))
def then_exact_listing(market_ctx, symbol, price, abundance):
    assert market_ctx['exact'] == (symbol, price, abundance)


@then(parsers.parse('the fallback listing should be "{symbol},{price:d},{abundance}"'))
def then_fallback_listing(market_ctx, symbol, price, abundance):
    assert market_ctx['fallback'] == (symbol, price, abundance)
