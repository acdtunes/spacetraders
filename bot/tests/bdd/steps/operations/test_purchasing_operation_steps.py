from types import SimpleNamespace

from pytest_bdd import scenarios, given, when, then, parsers

from spacetraders_bot.operations import purchasing

scenarios('../../features/operations/purchasing_operations.feature')


class FakePurchaseShip:
    def __init__(self, location="X1-TEST-A1"):
        self.ship_symbol = "PURCHASER"
        self.location = location
        self.cargo_units = 0
        self.navigate_calls = []
        self.dock_calls = 0

    def get_status(self):
        return {
            "nav": {"waypointSymbol": self.location, "systemSymbol": "X1-TEST", "status": "IN_ORBIT"},
            "cargo": {"capacity": 100, "units": 0},
            "fuel": {"capacity": 100, "current": 100},
            "engine": {"speed": 10},  # Add engine for SmartNavigator
            "frame": {"integrity": 1.0},  # Add frame for health check
            "registration": {"role": "HAULER"}  # Add role for health check
        }

    def navigate(self, waypoint=None, destination=None, flight_mode=None, auto_refuel=True):
        """Navigate ship (supports both old and new signature)."""
        dest = waypoint or destination
        self.navigate_calls.append(dest)
        self.location = dest
        return True

    def orbit(self):
        """Mock orbit method."""
        return True

    def dock(self):
        self.dock_calls += 1
        return True


class FailNavigateShip(FakePurchaseShip):
    def navigate(self, waypoint=None, destination=None, flight_mode=None, auto_refuel=True):
        """Navigate ship that always fails (supports both old and new signature)."""
        dest = waypoint or destination
        self.navigate_calls.append(dest)
        return False


class FakePurchaseAPI:
    def __init__(self, price=1000, credits=5000):
        self.price = price
        self.credits = credits
        self.purchase_calls = []
        self.fail_purchase = False

    def get(self, path):
        return {
            "data": {
                "ships": [
                    {
                        "type": "HEAVY_FREIGHTER",
                        "purchasePrice": self.price,
                    }
                ]
            }
        }

    def get_agent(self):
        return {"credits": self.credits}

    def list_waypoints(self, system_symbol, limit=20, page=1):
        """Mock waypoints for SmartNavigator graph building."""
        # Return minimal waypoint data for graph building
        waypoints = [
            {
                'symbol': 'X1-TEST-A1',
                'type': 'PLANET',
                'x': 0,
                'y': 0,
                'traits': [],
                'orbitals': []
            },
            {
                'symbol': 'X1-TEST-B1',
                'type': 'ORBITAL_STATION',
                'x': 100,
                'y': 100,
                'traits': [{'symbol': 'MARKETPLACE'}, {'symbol': 'SHIPYARD'}],
                'orbits': 'X1-TEST-A1'
            }
        ]
        return {'data': waypoints, 'meta': {'total': len(waypoints), 'page': page, 'limit': limit}}

    def post(self, path, payload):
        if path == "/my/ships":
            if self.fail_purchase:
                return {}
            self.purchase_calls.append(payload)
            self.credits -= self.price
            index = len(self.purchase_calls)
            return {
                "data": {
                    "ship": {"symbol": f"NEW-SHIP-{index}"},
                    "transaction": {"totalPrice": self.price},
                    "agent": {"credits": self.credits},
                }
            }
        return {}


def patch_purchase_helpers(monkeypatch, captain_events):
    monkeypatch.setattr(purchasing, "setup_logging", lambda *a, **k: "logfile.log")
    monkeypatch.setattr(purchasing, "get_captain_logger", lambda *_: captain_events)
    monkeypatch.setattr(
        purchasing,
        "log_captain_event",
        lambda writer, entry_type, **kwargs: writer.append((entry_type, kwargs)),
    )


@given(parsers.parse('a purchasing context with ship type "{ship_type}"'), target_fixture='purchase_ctx')
def given_purchase_context(monkeypatch, ship_type):
    captain_events = []
    patch_purchase_helpers(monkeypatch, captain_events)
    context = {
        'captain_events': captain_events,
        'ship_type': ship_type,
    }
    return context


@given('the purchasing ship is already docked at "X1-TEST-B1"')
def given_ship_docked(purchase_ctx):
    context = purchase_ctx
    context['ship'] = FakePurchaseShip(location='X1-TEST-B1')
    return context


@given('the purchasing ship is away from "X1-TEST-B1" and cannot navigate')
def given_ship_cannot_navigate(purchase_ctx):
    context = purchase_ctx
    context['ship'] = FailNavigateShip(location='X1-TEST-A1')
    return context


@given(parsers.parse('the shipyard lists the ship for {price:d} credits with {credits:d} credits available'))
def given_shipyard_listing(purchase_ctx, price, credits):
    context = purchase_ctx
    context['api'] = FakePurchaseAPI(price=price, credits=credits)
    return context


@given('the shipyard purchase call returns no data')
def given_shipyard_post_failure(purchase_ctx):
    context = purchase_ctx
    api = FakePurchaseAPI(price=1000, credits=5000)
    api.fail_purchase = True
    context['api'] = api
    context['ship'] = FakePurchaseShip(location='X1-TEST-B1')
    return context


@when(parsers.parse('the purchase operation runs for {quantity:d} ships with budget {budget:d}'))
def when_purchase_runs(purchase_ctx, quantity, budget, capsys):
    context = purchase_ctx
    api = context.get('api', FakePurchaseAPI())
    ship = context.get('ship', FakePurchaseShip())
    args = SimpleNamespace(
        player_id=1,
        ship=ship.ship_symbol,
        shipyard='X1-TEST-B1',
        ship_type=context['ship_type'],
        max_budget=budget,
        quantity=quantity,
        log_level='INFO',
        buy_from='X1-TEST-B1',
    )

    result = purchasing.purchase_ship_operation(args, api=api, ship=ship, captain_logger=context['captain_events'])
    context['result'] = result
    context['api'] = api
    context['ship'] = ship
    captured = capsys.readouterr()
    context['stdout'] = captured.out
    return context


@then('the purchase should complete with 2 new ships')
def then_purchase_success(purchase_ctx):
    context = purchase_ctx
    assert context['result'] == 0
    assert len(context['api'].purchase_calls) == 2
    assert any(event[0] == 'OPERATION_COMPLETED' for event in context['captain_events'])


@then('the purchase should fail with an error log')
def then_purchase_error(purchase_ctx):
    context = purchase_ctx
    assert context['result'] == 1
    assert any(event[0] == 'CRITICAL_ERROR' for event in context['captain_events'])


@then(parsers.parse('the purchase should fail with message "{message}"'))
def then_purchase_failure_message(purchase_ctx, message):
    context = purchase_ctx
    assert context['result'] == 1
    assert message in context.get('stdout', '')


@then('no purchase requests should be sent')
def then_no_purchase_requests(purchase_ctx):
    assert not purchase_ctx['api'].purchase_calls


@given(parsers.parse('a purchasing context with invalid quantity "{quantity}"'), target_fixture='validation_ctx')
def given_invalid_quantity_context(monkeypatch, quantity):
    context = {'quantity': quantity}
    return context


@given(parsers.parse('a purchasing context with invalid budget "{budget}"'), target_fixture='validation_ctx')
def given_invalid_budget_context(monkeypatch, budget):
    return {'budget': budget}


@when('the purchase operation validates arguments', target_fixture='validation_ctx')
def when_validate_args(validation_ctx):
    context = validation_ctx
    args = SimpleNamespace(
        player_id=1,
        ship='PURCHASER',
        shipyard='X1-TEST-B1',
        ship_type='HAULER',
        max_budget=context.get('budget', 5000),
        quantity=context.get('quantity', 1),
    )
    result = purchasing._validate_purchase_args(args)
    context['validation_result'] = result
    return context


@then('validation should fail')
def then_validation_fail(validation_ctx):
    assert validation_ctx['validation_result'] is None


@given(parsers.parse('a purchasing context missing the argument "{arg_name}"'), target_fixture='missing_ctx')
def given_missing_arg_context(arg_name):
    return {'missing_arg': arg_name}


@when('the purchase operation validates arguments with missing data', target_fixture='missing_ctx')
def when_validate_missing_arg(missing_ctx):
    template = {
        'player_id': 1,
        'ship': 'PURCHASER',
        'shipyard': 'X1-TEST-B1',
        'ship_type': 'HAULER',
        'max_budget': 5000,
    }
    template.pop(missing_ctx['missing_arg'], None)
    args = SimpleNamespace(**template)
    result = purchasing._validate_purchase_args(args)
    missing_ctx['validation_result'] = result
    return missing_ctx


@then('validation should fail for missing argument')
def then_validation_missing_fail(missing_ctx):
    assert missing_ctx['validation_result'] is None
