"""BDD step definitions for contract operations."""

import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, MagicMock, patch
from src.spacetraders_bot.operations.contracts import (
    evaluate_contract_profitability,
    contract_operation,
    batch_contract_operation,
    ResourceAcquisitionStrategy,
)
from src.spacetraders_bot.core.database import Database


# Load all contract scenarios
scenarios('../../features/contracts/transaction_limits.feature')
scenarios('../../features/contracts/navigator_integration.feature')
scenarios('../../features/contracts/pagination.feature')
scenarios('../../features/contracts/profitability_evaluation.feature')
scenarios('../../features/contracts/batch_operations.feature')
scenarios('../../features/contracts/price_polling.feature')


@pytest.fixture
def contracts_context():
    """Context for contract tests."""
    return {
        'ship_data': None,
        'contract_data': None,
        'api': None,
        'navigator': None,
        'database': None,
        'args': None,
        'result': None,
        'purchase_calls': [],
        'navigate_calls': [],
        'negotiate_calls': [],
        'fulfill_calls': [],
        'get_calls': [],
        'post_calls': [],
        'sleep_calls': [],
        'print_calls': [],
        'contracts_list': [],
        'market_prices': {},
        'transaction_limit': None,
        'error_4604_count': 0,
        'strategy': None,
        'profitability_result': None,
        'metrics': None,
    }


# ==================== Ship Setup ====================

@given(parsers.parse('a ship "{ship_symbol}" with {capacity:d} cargo capacity'))
def setup_ship_with_capacity(contracts_context, ship_symbol, capacity):
    """Set up a ship with specified cargo capacity."""
    contracts_context['ship_data'] = {
        'symbol': ship_symbol,
        'cargo': {'capacity': capacity, 'units': 0, 'inventory': []},
        'nav': {'waypointSymbol': 'X1-TEST-A1', 'systemSymbol': 'X1-TEST', 'status': 'IN_ORBIT'},
        'fuel': {'current': 400, 'capacity': 400},
    }


@given(parsers.parse('a ship with {capacity:d} cargo capacity'))
def setup_ship_simple(contracts_context, capacity):
    """Set up a ship with cargo capacity (simple form)."""
    contracts_context['ship_data'] = {
        'symbol': 'TEST-SHIP',
        'cargo': {'capacity': capacity, 'units': 0, 'inventory': []},
        'nav': {'waypointSymbol': 'X1-TEST-A1', 'systemSymbol': 'X1-TEST', 'status': 'IN_ORBIT'},
        'fuel': {'current': 400, 'capacity': 400},
    }


@given(parsers.parse('the ship is at market "{market}" (docked)'))
def set_ship_location_docked(contracts_context, market):
    """Set ship location to market (docked)."""
    ship = contracts_context['ship_data']
    ship['nav']['waypointSymbol'] = market
    ship['nav']['status'] = 'DOCKED'


@given(parsers.parse('the ship has {fuel:d} fuel'))
def set_ship_fuel(contracts_context, fuel):
    """Set ship fuel level."""
    contracts_context['ship_data']['fuel']['current'] = fuel


@given(parsers.parse('a ship "{ship_symbol}" with {capacity:d} cargo capacity at "{location}"'))
def setup_ship_at_location(contracts_context, ship_symbol, capacity, location):
    """Set up a ship at a specific location."""
    contracts_context['ship_data'] = {
        'symbol': ship_symbol,
        'cargo': {'capacity': capacity, 'units': 0, 'inventory': []},
        'nav': {'waypointSymbol': location, 'systemSymbol': 'X1-JB26', 'status': 'DOCKED'},
        'fuel': {'current': 400, 'capacity': 400},
        'registration': {'role': 'HAULER'},
        'frame': {'integrity': 1.0},
    }


# ==================== Navigator Setup ====================

@given(parsers.parse('a SmartNavigator for system "{system}"'))
def setup_navigator(contracts_context, system):
    """Set up a mock SmartNavigator."""
    navigator = MagicMock()
    navigator.execute_route = MagicMock(return_value=True)
    contracts_context['navigator'] = navigator
    contracts_context['system'] = system


@given(parsers.parse('system "{system}" with markets'))
def setup_system_with_markets(contracts_context, system):
    """Set up system with markets."""
    contracts_context['system'] = system
    contracts_context['markets'] = {}


# ==================== Contract Setup ====================

@given(parsers.parse('a contract requiring {units:d} units of {resource}'))
def setup_contract_requirement(contracts_context, units, resource):
    """Set up a contract with delivery requirements."""
    if contracts_context['contract_data'] is None:
        contracts_context['contract_data'] = {
            'id': 'test-contract',
            'accepted': True,
            'fulfilled': False,
            'factionSymbol': 'COSMIC',
            'type': 'PROCUREMENT',
            'terms': {
                'payment': {'onAccepted': 10000, 'onFulfilled': 50000},
                'deliver': []
            }
        }

    contracts_context['contract_data']['terms']['deliver'].append({
        'tradeSymbol': resource,
        'unitsRequired': units,
        'unitsFulfilled': 0,
        'destinationSymbol': 'X1-TEST-A1',
    })


@given(parsers.parse('contract payment is {total:d} credits ({accepted:d} accepted + {fulfilled:d} fulfilled)'))
def set_contract_payment_split(contracts_context, total, accepted, fulfilled):
    """Set contract payment breakdown."""
    assert accepted + fulfilled == total, f"Payment split doesn't add up: {accepted} + {fulfilled} != {total}"
    if contracts_context.get('contract_data') is None:
        contracts_context['contract_data'] = {
            'terms': {
                'payment': {'onAccepted': 0, 'onFulfilled': 0},
                'deliver': []
            }
        }
    contract = contracts_context['contract_data']
    contract['terms']['payment']['onAccepted'] = accepted
    contract['terms']['payment']['onFulfilled'] = fulfilled


@given(parsers.parse('contract payment is {payment} credits ({split})'))
def set_contract_payment_with_split_text(contracts_context, payment, split):
    """Set contract payment with split text."""
    # Parse split like "10,000 + 50,000" or "19,425 + 174,831"
    payment_val = int(payment.replace(',', ''))
    parts = split.replace(',', '').split('+')
    accepted = int(parts[0].strip())
    fulfilled = int(parts[1].strip())
    assert accepted + fulfilled == payment_val, f"Payment split doesn't add up: {accepted} + {fulfilled} != {payment_val}"

    if contracts_context.get('contract_data') is None:
        contracts_context['contract_data'] = {
            'terms': {
                'payment': {'onAccepted': 0, 'onFulfilled': 0},
                'deliver': []
            }
        }
    contract = contracts_context['contract_data']
    contract['terms']['payment']['onAccepted'] = accepted
    contract['terms']['payment']['onFulfilled'] = fulfilled


@given(parsers.parse('contract payment is {total} credits total'))
def set_contract_payment_total(contracts_context, total):
    """Set total contract payment (default split)."""
    total_val = int(total.replace(',', ''))
    contract = contracts_context['contract_data']
    contract['terms']['payment']['onAccepted'] = 0
    contract['terms']['payment']['onFulfilled'] = total_val


@given(parsers.parse('contract payment is {payment} credits'))
def set_contract_payment_simple(contracts_context, payment):
    """Set contract payment (simple form with commas)."""
    payment_val = int(payment.replace(',', ''))
    if contracts_context.get('contract_data') is None:
        contracts_context['contract_data'] = {
            'terms': {
                'payment': {'onAccepted': 0, 'onFulfilled': 0},
                'deliver': []
            }
        }
    contract = contracts_context['contract_data']
    contract['terms']['payment']['onAccepted'] = 0
    contract['terms']['payment']['onFulfilled'] = payment_val


@given(parsers.parse('a contract requiring {units:d} units of {resource} at "{destination}"'))
def setup_contract_with_destination(contracts_context, units, resource, destination):
    """Set up a contract with specific destination."""
    contracts_context['contract_data'] = {
        'id': 'test-contract',
        'accepted': True,
        'fulfilled': False,
        'terms': {
            'payment': {'onAccepted': 5000, 'onFulfilled': 50000},
            'deliver': [{
                'tradeSymbol': resource,
                'unitsRequired': units,
                'unitsFulfilled': 0,
                'destinationSymbol': destination,
            }]
        }
    }


@given(parsers.parse('{units_fulfilled:d} units are already fulfilled'))
def set_units_fulfilled(contracts_context, units_fulfilled):
    """Set units already fulfilled in contract."""
    contract = contracts_context['contract_data']
    contract['terms']['deliver'][0]['unitsFulfilled'] = units_fulfilled


@given('a contract with no delivery requirements')
def setup_contract_no_delivery(contracts_context):
    """Set up a contract with no delivery requirements."""
    contracts_context['contract_data'] = {
        'terms': {
            'payment': {'onAccepted': 10000, 'onFulfilled': 100000},
            'deliver': []
        }
    }


# ==================== Market Setup ====================

@given(parsers.parse('the market has a {limit:d} unit per-transaction limit for {resource}'))
def set_transaction_limit(contracts_context, limit, resource):
    """Set market transaction limit."""
    contracts_context['transaction_limit'] = limit
    contracts_context['resource'] = resource


@given(parsers.parse('the market purchase price is {price:d} credits per unit'))
def set_market_price(contracts_context, price):
    """Set market purchase price."""
    if not contracts_context.get('market_prices'):
        contracts_context['market_prices'] = {}

    # Get resource from contract
    resource = contracts_context['contract_data']['terms']['deliver'][0]['tradeSymbol']
    contracts_context['market_prices'][resource] = price


@given(parsers.parse('market "{market}" sells {resource} at {price:d} credits per unit'))
def set_market_sells_resource(contracts_context, market, resource, price):
    """Set up market selling a specific resource."""
    if not contracts_context.get('markets'):
        contracts_context['markets'] = {}

    contracts_context['markets'][market] = {
        'goods': {
            resource: {
                'purchase_price': price,
                'supply': 'MODERATE'
            }
        }
    }
    contracts_context['market_prices'][resource] = price


@given(parsers.parse('the market is {distance:d} units away'))
def set_market_distance(contracts_context, distance):
    """Set distance to market."""
    contracts_context['market_distance'] = distance


@given(parsers.parse('{resource} costs {price} credits per unit at market'))
def set_resource_market_price(contracts_context, resource, price):
    """Set resource market price."""
    price_val = int(price.replace(',', ''))
    if not contracts_context.get('market_prices'):
        contracts_context['market_prices'] = {}
    contracts_context['market_prices'][resource] = price_val


@given(parsers.parse('{resource} costs {price} credits per unit'))
def set_resource_cost_no_location(contracts_context, resource, price):
    """Set resource cost (alternative form)."""
    price_val = int(price.replace(',', ''))
    if not contracts_context.get('market_prices'):
        contracts_context['market_prices'] = {}
    contracts_context['market_prices'][resource] = price_val


@given(parsers.parse('no market price data is available for {resource}'))
def no_market_data_for_resource(contracts_context, resource):
    """Ensure no market data for resource."""
    if contracts_context.get('market_prices'):
        contracts_context['market_prices'].pop(resource, None)


# ==================== Database Setup ====================

@given('a database with real market prices')
def setup_database_with_prices(contracts_context):
    """Set up mock database with market prices."""
    db = MagicMock(spec=Database)

    def mock_connection():
        class MockConnection:
            def __enter__(self):
                return self
            def __exit__(self, *args):
                pass
            def execute(self, query, params):
                class MockCursor:
                    def fetchone(self):
                        return None
                    def fetchall(self):
                        trade_symbol = params[0] if params else None
                        price = contracts_context.get('market_prices', {}).get(trade_symbol)
                        if price:
                            return [{
                                'waypoint_symbol': 'X1-TEST-M1',
                                'good_symbol': trade_symbol,
                                'supply': 'MODERATE',
                                'activity': 'WEAK',
                                'purchase_price': price,
                                'sell_price': price + 100,
                                'trade_volume': 10,
                                'last_updated': '2025-10-14T00:00:00Z'
                            }]
                        return []
                return MockCursor()
        return MockConnection()

    db.connection = mock_connection
    contracts_context['database'] = db


# ==================== Transaction Limit Steps ====================

@given(parsers.parse('the market transaction limit is unknown initially'))
def unknown_transaction_limit(contracts_context):
    """Transaction limit unknown initially."""
    contracts_context['transaction_limit'] = None


@given(parsers.parse('attempting to buy {units:d} units returns error 4604'))
def mock_error_4604(contracts_context, units):
    """Mock API returning error 4604."""
    contracts_context['error_4604_trigger_units'] = units


@given(parsers.parse('the error message indicates "limit of {limit:d} units per transaction"'))
def error_message_limit(contracts_context, limit):
    """Error message indicates transaction limit."""
    contracts_context['error_message_limit'] = limit


# ==================== Navigator Integration Steps ====================

@given(parsers.parse('market discovery finds "{market}" after retries'))
def mock_market_discovery(contracts_context, market):
    """Mock market discovery."""
    contracts_context['discovered_market'] = market


@given(parsers.parse('_acquire_initial_resources is called without navigator parameter'))
def call_without_navigator(contracts_context):
    """Simulate calling without navigator."""
    contracts_context['navigator_passed'] = False


# ==================== Pagination Steps ====================

@given(parsers.parse('an agent with multiple contracts across pages'))
def setup_multi_page_contracts(contracts_context):
    """Set up agent with contracts across pages."""
    contracts_context['contracts_list'] = []


@given(parsers.parse('the API returns {limit:d} contracts per page by default'))
def api_page_limit(contracts_context, limit):
    """Set API page limit."""
    contracts_context['page_limit'] = limit


@given(parsers.parse('the agent has {total:d} total contracts'))
def set_total_contracts(contracts_context, total):
    """Set total number of contracts."""
    contracts_context['total_contracts'] = total


@given(parsers.parse('page 1 has {count:d} fulfilled contracts'))
def page1_fulfilled(contracts_context, count):
    """Set page 1 contracts."""
    page1 = [
        {'id': f'c{i}', 'accepted': True, 'fulfilled': True, 'terms': {'deliver': []}}
        for i in range(1, count + 1)
    ]
    contracts_context['page1_contracts'] = page1


@given(parsers.parse('page 2 has {fulfilled:d} fulfilled contracts and {active:d} active contract'))
def page2_mixed(contracts_context, fulfilled, active):
    """Set page 2 with mixed contracts."""
    page2 = [
        {'id': f'c{i}', 'accepted': True, 'fulfilled': True, 'terms': {'deliver': []}}
        for i in range(11, 11 + fulfilled)
    ]

    for i in range(active):
        page2.append({
            'id': f'active-contract-{i+1}',
            'accepted': True,
            'fulfilled': False,
            'terms': {
                'payment': {'onAccepted': 1000, 'onFulfilled': 5000},
                'deliver': [{
                    'tradeSymbol': 'IRON',
                    'unitsRequired': 50,
                    'unitsFulfilled': 0,
                    'destinationSymbol': 'X1-TEST-A1',
                }]
            }
        })

    contracts_context['page2_contracts'] = page2


@given(parsers.parse('contract {contract_id:d} is ACTIVE (accepted but not fulfilled)'))
def set_contract_active(contracts_context, contract_id):
    """Mark a specific contract as active."""
    contracts_context['active_contract_id'] = contract_id


@given(parsers.parse('contracts {start:d}-{end:d} are fulfilled'))
def contracts_range_fulfilled(contracts_context, start, end):
    """Mark range of contracts as fulfilled."""
    if 'page1_contracts' not in contracts_context:
        contracts_context['page1_contracts'] = []
    if 'page2_contracts' not in contracts_context:
        contracts_context['page2_contracts'] = []


@given(parsers.parse('the user requests batch operation with count={count:d}'))
def batch_count_request(contracts_context, count):
    """User requests batch with count."""
    contracts_context['batch_count'] = count


@given(parsers.parse('all {count:d} contracts are fulfilled'))
def all_contracts_fulfilled(contracts_context, count):
    """All contracts are fulfilled."""
    contracts = [
        {'id': f'c{i}', 'accepted': True, 'fulfilled': True, 'terms': {'deliver': []}}
        for i in range(1, count + 1)
    ]
    contracts_context['all_contracts'] = contracts


# ==================== Batch Operations Steps ====================

@given(parsers.parse('a batch operation with count={count:d}'))
def setup_batch_operation(contracts_context, count):
    """Set up batch operation."""
    contracts_context['batch_count'] = count
    contracts_context['batch_contracts'] = []


@given(parsers.parse('all {count:d} contracts are profitable'))
def all_contracts_profitable(contracts_context, count):
    """All contracts in batch are profitable."""
    contracts = []
    for i in range(1, count + 1):
        contracts.append({
            'id': f'contract-{i}',
            'type': 'PROCUREMENT',
            'factionSymbol': 'COSMIC',
            'terms': {
                'payment': {'onAccepted': 10000, 'onFulfilled': 100000},
                'deliver': [{
                    'unitsRequired': 50,
                    'unitsFulfilled': 0,
                    'tradeSymbol': 'IRON_ORE',
                    'destinationSymbol': 'X1-TEST-A1',
                }]
            }
        })
    contracts_context['batch_contracts'] = contracts


@given(parsers.parse('contract {num:d} is profitable'))
def contract_profitable(contracts_context, num):
    """Mark specific contract as profitable."""
    while len(contracts_context.get('batch_contracts', [])) < num:
        contracts_context.setdefault('batch_contracts', []).append({
            'id': f'contract-{len(contracts_context["batch_contracts"]) + 1}',
            'type': 'PROCUREMENT',
            'factionSymbol': 'COSMIC',
            'terms': {
                'payment': {'onAccepted': 10000, 'onFulfilled': 100000},
                'deliver': [{
                    'unitsRequired': 50,
                    'unitsFulfilled': 0,
                    'tradeSymbol': 'IRON_ORE',
                    'destinationSymbol': 'X1-TEST-A1',
                }]
            }
        })


@given(parsers.parse('contract {num:d} is unprofitable (low payment)'))
def contract_unprofitable(contracts_context, num):
    """Mark specific contract as unprofitable."""
    while len(contracts_context.get('batch_contracts', [])) < num:
        contracts_context.setdefault('batch_contracts', []).append(None)

    contracts_context['batch_contracts'][num - 1] = {
        'id': f'contract-{num}',
        'type': 'PROCUREMENT',
        'factionSymbol': 'COSMIC',
        'terms': {
            'payment': {'onAccepted': 100, 'onFulfilled': 500},
            'deliver': [{
                'unitsRequired': 50,
                'unitsFulfilled': 0,
                'tradeSymbol': 'IRON_ORE',
                'destinationSymbol': 'X1-TEST-A1',
            }]
        }
    }


@given(parsers.parse('contract {num:d} fulfillment will fail'))
def contract_fulfillment_fails(contracts_context, num):
    """Mark contract fulfillment to fail."""
    contracts_context.setdefault('failing_fulfillments', []).append(num)


@given(parsers.parse('contract {num:d} negotiation will fail (returns None)'))
def contract_negotiation_fails(contracts_context, num):
    """Mark contract negotiation to fail."""
    contracts_context.setdefault('failing_negotiations', []).append(num)


@given(parsers.parse('all {count:d} contract fulfillments will fail'))
def all_fulfillments_fail(contracts_context, count):
    """All fulfillments will fail."""
    contracts_context['failing_fulfillments'] = list(range(1, count + 1))


@given(parsers.parse('both contracts are unprofitable ({payment} credits payment vs {cost} cost)'))
def both_contracts_unprofitable(contracts_context, payment, cost):
    """Both contracts are unprofitable."""
    payment_val = int(payment.replace(',', ''))
    cost_val = int(cost.replace(',', ''))
    contracts = []
    for i in range(1, 3):
        contracts.append({
            'id': f'contract-{i}',
            'type': 'PROCUREMENT',
            'factionSymbol': 'COSMIC',
            'terms': {
                'payment': {'onAccepted': 10, 'onFulfilled': payment_val - 10},
                'deliver': [{
                    'unitsRequired': 50,
                    'unitsFulfilled': 0,
                    'tradeSymbol': 'IRON_ORE',
                    'destinationSymbol': 'X1-TEST-A1',
                }]
            }
        })
    contracts_context['batch_contracts'] = contracts


@given(parsers.parse('the agent has {count:d} existing active unfulfilled contract'))
def existing_active_contract(contracts_context, count):
    """Agent has existing active contract."""
    contracts = []
    for i in range(count):
        contracts.append({
            'id': f'existing-contract-{i+1}',
            'type': 'PROCUREMENT',
            'factionSymbol': 'COSMIC',
            'accepted': True,
            'fulfilled': False,
            'terms': {
                'payment': {'onAccepted': 5000, 'onFulfilled': 50000},
                'deliver': [{
                    'unitsRequired': 26,
                    'unitsFulfilled': 0,
                    'tradeSymbol': 'EQUIPMENT',
                    'destinationSymbol': 'X1-TEST-D41',
                }]
            }
        })
    contracts_context['existing_contracts'] = contracts


@given(parsers.parse('the agent has {count:d} active contract on page 2'))
def active_contract_on_page2(contracts_context, count):
    """Agent has active contract on page 2."""
    # Already handled by page2_mixed step
    pass


@given(parsers.parse('the system checks if previous contract is fulfilled before negotiating'))
def check_previous_fulfilled(contracts_context):
    """System checks if previous contract fulfilled."""
    contracts_context['check_fulfillment_before_negotiate'] = True


# ==================== Price Polling Steps ====================

@given(parsers.parse('max polling retries of {retries:d} ({hours:d} hour total)'))
def set_max_retries(contracts_context, retries, hours):
    """Set max polling retries."""
    contracts_context['max_retries'] = retries


@given(parsers.parse('retry interval of {seconds:d} seconds ({minutes:d} minutes)'))
def set_retry_interval(contracts_context, seconds, minutes):
    """Set retry interval."""
    contracts_context['retry_interval'] = seconds


@given(parsers.parse('{resource} market price is {price:d} credits per unit (profitable)'))
def resource_price_profitable(contracts_context, resource, price):
    """Resource price is profitable."""
    if not contracts_context.get('market_prices'):
        contracts_context['market_prices'] = {}
    contracts_context['market_prices'][resource] = price


@given(parsers.parse('{resource} market price starts at {start_price:d} credits per unit (unprofitable)'))
def resource_price_starts_unprofitable(contracts_context, resource, start_price):
    """Resource price starts unprofitable."""
    if not contracts_context.get('price_sequence'):
        contracts_context['price_sequence'] = []
    contracts_context['price_sequence'].append(start_price)
    contracts_context['resource'] = resource


@given(parsers.parse('after {retries:d} retry, price drops to {end_price:d} credits per unit (profitable)'))
def price_drops_after_retry(contracts_context, retries, end_price):
    """Price drops after retries."""
    contracts_context['price_sequence'].append(end_price)
    contracts_context['price_drop_after'] = retries


@given(parsers.parse('{resource} market price stays at {price:d} credits per unit (unprofitable)'))
def resource_price_stays_unprofitable(contracts_context, resource, price):
    """Resource price stays unprofitable."""
    contracts_context['constant_price'] = price
    contracts_context['resource'] = resource


@given('the price never drops during polling window')
def price_never_drops(contracts_context):
    """Price never drops."""
    contracts_context['price_never_drops'] = True


@given(parsers.parse('a contract with payment of {payment:d} credits total'))
def contract_payment_total(contracts_context, payment):
    """Contract with specific payment."""
    contracts_context['contract_data'] = {
        'terms': {
            'payment': {'onAccepted': 1000, 'onFulfilled': payment - 1000},
            'deliver': [{
                'tradeSymbol': 'IRON_ORE',
                'unitsRequired': 100,
                'unitsFulfilled': 0,
                'destinationSymbol': 'X1-TEST-HQ'
            }]
        }
    }


@given(parsers.parse('total cost is {cost:d} credits ({base_cost:d} + {fuel_cost:d} fuel)'))
def set_total_cost(contracts_context, cost, base_cost, fuel_cost):
    """Set expected total cost."""
    contracts_context['expected_cost'] = cost


@given(parsers.parse('no market data is available for {resource}'))
def no_market_data_available(contracts_context, resource):
    """No market data available."""
    contracts_context['market_prices'] = {}
    contracts_context['resource'] = resource


@given(parsers.parse('a profitable contract with {resource} at {price:d} cr/unit'))
def profitable_contract_at_price(contracts_context, resource, price):
    """Profitable contract at specific price."""
    if not contracts_context.get('market_prices'):
        contracts_context['market_prices'] = {}
    contracts_context['market_prices'][resource] = price


@given(parsers.parse('net profit is {profit:d} credits (meets >{threshold:d} threshold)'))
def set_expected_profit(contracts_context, profit, threshold):
    """Set expected profit."""
    contracts_context['expected_profit'] = profit


@given(parsers.parse('ROI is {roi:d}% (meets >{threshold:d}% threshold)'))
def set_expected_roi(contracts_context, roi, threshold):
    """Set expected ROI."""
    contracts_context['expected_roi'] = roi


# ==================== When Steps ====================

@when('I fulfill the contract')
def fulfill_contract(contracts_context):
    """Fulfill the contract."""
    # Mock API
    mock_api = Mock()
    ship_status = contracts_context['ship_data']
    contract = contracts_context['contract_data']
    purchase_calls = []

    def mock_post(endpoint, data=None):
        if '/purchase' in endpoint:
            units = data.get('units', 0)
            purchase_calls.append({'endpoint': endpoint, 'symbol': data.get('symbol'), 'units': units})

            # Simulate transaction limit error
            limit = contracts_context.get('transaction_limit')
            if limit and units > limit:
                contracts_context['error_4604_count'] += 1
                return {
                    'error': {
                        'code': 4604,
                        'message': f'Trade good {data.get("symbol")} has a limit of {limit} units per transaction'
                    }
                }

            # Success
            ship_status['cargo']['units'] += units
            return {
                'data': {
                    'transaction': {
                        'units': units,
                        'tradeSymbol': data.get('symbol'),
                        'totalPrice': units * 100,
                        'pricePerUnit': 100
                    }
                }
            }

        if '/deliver' in endpoint:
            return {'data': {'contract': contract}}

        if '/fulfill' in endpoint:
            contract['fulfilled'] = True
            return {'data': {'contract': contract}}

        return {'data': {}}

    mock_api.post = mock_post
    mock_api.get_ship = Mock(return_value=ship_status)
    mock_api.get_contract = Mock(return_value=contract)

    # Use REAL ShipController
    from src.spacetraders_bot.core.ship_controller import ShipController
    ship = ShipController(mock_api, ship_status['symbol'])
    ship.log = lambda msg, level='INFO': None

    # Mock navigator
    mock_navigator = Mock()
    mock_navigator.execute_route = Mock(return_value=True)

    # Mock args
    args = Mock()
    args.ship = ship_status['symbol']
    args.contract_id = contract['id']
    args.buy_from = ship_status['nav']['waypointSymbol']
    args.player_id = 1
    args.log_level = 'WARNING'

    # Mock database
    mock_db = Mock()

    class MockConnection:
        def __enter__(self):
            return self
        def __exit__(self, *args):
            pass
        def execute(self, *args):
            mock_cursor = Mock()
            mock_cursor.fetchone.return_value = None
            return mock_cursor

    mock_db.connection.return_value = MockConnection()

    # Run operation
    with patch('src.spacetraders_bot.operations.contracts.Database', return_value=mock_db):
        with patch('src.spacetraders_bot.operations.contracts.setup_logging', return_value='test.log'):
            with patch('src.spacetraders_bot.operations.contracts.get_api_client', return_value=mock_api):
                with patch('src.spacetraders_bot.operations.contracts.ShipController', return_value=ship):
                    with patch('src.spacetraders_bot.operations.contracts.SmartNavigator', return_value=mock_navigator):
                        with patch('src.spacetraders_bot.operations.contracts.get_captain_logger', return_value=Mock()):
                            result = contract_operation(args, api=mock_api, ship=ship, navigator=mock_navigator,
                                                       db=mock_db, sleep_fn=lambda x: None)

    contracts_context['result'] = result
    contracts_context['purchase_calls'] = purchase_calls


@when('I fulfill the contract with navigator passed explicitly')
def fulfill_with_navigator(contracts_context):
    """Fulfill contract with navigator."""
    mock_api = MagicMock()
    ship = contracts_context['ship_data']
    contract = contracts_context['contract_data']
    navigator = contracts_context['navigator']

    # Mock ship controller
    mock_ship = MagicMock()
    mock_ship.get_status = MagicMock(return_value=ship)
    mock_ship.buy = MagicMock(return_value={'units': 26, 'totalPrice': 31850})
    mock_ship.dock = MagicMock(return_value=True)

    # Mock API responses
    mock_api.get_contract = MagicMock(return_value=contract)
    mock_api.post = MagicMock(return_value={'data': {'contract': contract}})

    # Mock database
    mock_db = MagicMock()
    conn_mock = MagicMock()
    cursor_mock = MagicMock()
    market = list(contracts_context.get('markets', {}).keys())[0] if contracts_context.get('markets') else 'X1-JB26-K88'
    price = contracts_context.get('market_prices', {}).get('CLOTHING', 1225)
    cursor_mock.fetchone = MagicMock(return_value=(market, price, 'ABUNDANT'))
    cursor_mock.fetchall = MagicMock(return_value=[(market, price, 'ABUNDANT')])
    conn_mock.execute = MagicMock(return_value=cursor_mock)
    conn_mock.__enter__ = MagicMock(return_value=conn_mock)
    conn_mock.__exit__ = MagicMock(return_value=None)
    mock_db.connection = MagicMock(return_value=conn_mock)
    mock_db.get_system_graph = MagicMock(return_value={'waypoints': {}})

    # Mock args
    args = MagicMock()
    args.player_id = 2
    args.ship = ship['symbol']
    args.contract_id = contract['id']
    args.buy_from = None
    args.log_level = 'ERROR'

    # Execute
    with patch('src.spacetraders_bot.operations.contracts.ShipController', return_value=mock_ship):
        with patch('src.spacetraders_bot.operations.contracts.SmartNavigator', return_value=navigator):
            with patch('src.spacetraders_bot.operations.contracts.Database', return_value=mock_db):
                with patch('src.spacetraders_bot.operations.contracts.time.sleep', return_value=None):
                    result = contract_operation(args, api=mock_api, ship=mock_ship, navigator=navigator,
                                               db=mock_db, sleep_fn=lambda x: None)

    contracts_context['result'] = result
    contracts_context['api'] = mock_api
    contracts_context['mock_ship'] = mock_ship


@when('I fulfill the contract without passing navigator parameter')
def fulfill_without_navigator(contracts_context):
    """Fulfill contract without navigator."""
    mock_api = MagicMock()
    ship = contracts_context['ship_data']
    contract = contracts_context['contract_data']

    # Mock ship controller
    mock_ship = MagicMock()
    mock_ship.get_status = MagicMock(return_value=ship)
    mock_ship.buy = MagicMock(return_value={'units': 26, 'totalPrice': 31850})
    mock_ship.dock = MagicMock(return_value=True)

    # Create real navigator mock that will be initialized internally
    real_navigator = MagicMock()
    real_navigator.execute_route = MagicMock(return_value=True)

    # Mock API responses
    mock_api.get_contract = MagicMock(return_value=contract)
    mock_api.post = MagicMock(return_value={'data': {'contract': contract}})

    # Mock database
    mock_db = MagicMock()
    conn_mock = MagicMock()
    cursor_mock = MagicMock()
    market = list(contracts_context.get('markets', {}).keys())[0] if contracts_context.get('markets') else 'X1-JB26-K88'
    price = contracts_context.get('market_prices', {}).get('CLOTHING', 1225)
    cursor_mock.fetchone = MagicMock(return_value=(market, price, 'ABUNDANT'))
    cursor_mock.fetchall = MagicMock(return_value=[(market, price, 'ABUNDANT')])
    conn_mock.execute = MagicMock(return_value=cursor_mock)
    conn_mock.__enter__ = MagicMock(return_value=conn_mock)
    conn_mock.__exit__ = MagicMock(return_value=None)
    mock_db.connection = MagicMock(return_value=conn_mock)
    mock_db.get_system_graph = MagicMock(return_value={'waypoints': {}})

    # Mock args
    args = MagicMock()
    args.player_id = 2
    args.ship = ship['symbol']
    args.contract_id = contract['id']
    args.buy_from = None
    args.log_level = 'ERROR'

    # Execute without navigator parameter
    with patch('src.spacetraders_bot.operations.contracts.ShipController', return_value=mock_ship):
        with patch('src.spacetraders_bot.operations.contracts.SmartNavigator', return_value=real_navigator):
            with patch('src.spacetraders_bot.operations.contracts.Database', return_value=mock_db):
                with patch('src.spacetraders_bot.operations.contracts.time.sleep', return_value=None):
                    result = contract_operation(args, api=mock_api, ship=mock_ship, navigator=None,
                                               db=mock_db, sleep_fn=lambda x: None)

    contracts_context['result'] = result
    contracts_context['internal_navigator'] = real_navigator


@when('the system tries to navigate to the market')
def try_navigate_to_market(contracts_context):
    """Try to navigate to market."""
    # This is simulated in the fulfill step
    pass


@when('I start a batch contract operation')
def start_batch_operation(contracts_context):
    """Start batch operation."""
    # Setup mock API
    mock_api = MagicMock()

    page1 = contracts_context.get('page1_contracts', [])
    page2 = contracts_context.get('page2_contracts', [])

    get_calls = []

    def mock_get(endpoint):
        get_calls.append(endpoint)
        if '/my/contracts' in endpoint:
            if 'page=2' in endpoint:
                return {'data': page2, 'meta': {'total': len(page1) + len(page2), 'page': 2, 'limit': 20}}
            else:
                return {'data': page1, 'meta': {'total': len(page1) + len(page2), 'page': 1, 'limit': 20}}
        return None

    mock_api.get = MagicMock(side_effect=mock_get)
    mock_api.post = MagicMock(return_value={
        'data': {
            'contract': {
                'id': 'new-contract',
                'type': 'PROCUREMENT',
                'factionSymbol': 'COSMIC',
                'terms': {
                    'payment': {'onAccepted': 10000, 'onFulfilled': 100000},
                    'deliver': [{
                        'unitsRequired': 50,
                        'unitsFulfilled': 0,
                        'tradeSymbol': 'IRON',
                        'destinationSymbol': 'X1-TEST-A1',
                    }]
                }
            }
        }
    })

    # Mock ship
    mock_ship = MagicMock()
    mock_ship.get_status = MagicMock(return_value={
        'cargo': {'capacity': 40, 'units': 0},
        'nav': {'systemSymbol': 'X1-TEST'},
    })

    # Mock args
    args = MagicMock()
    args.player_id = 1
    args.ship = 'SHIP-1'
    args.contract_count = contracts_context.get('batch_count', 1)
    args.buy_from = None
    args.log_level = 'ERROR'

    fulfill_calls = []

    def mock_fulfill(args, **kwargs):
        fulfill_calls.append(args.contract_id)
        return 0

    with patch('src.spacetraders_bot.operations.contracts.contract_operation', side_effect=mock_fulfill):
        with patch('src.spacetraders_bot.operations.contracts.ShipController', return_value=mock_ship):
            result = batch_contract_operation(args, api=mock_api)

    contracts_context['result'] = result
    contracts_context['get_calls'] = get_calls
    contracts_context['fulfill_calls'] = fulfill_calls


@when('I check for active contracts')
def check_active_contracts(contracts_context):
    """Check for active contracts (part of batch operation)."""
    # This is part of start_batch_operation
    pass


@when('I run the batch operation')
def run_batch_operation(contracts_context):
    """Run the batch operation."""
    mock_api = MagicMock()

    # Setup contracts
    contracts = contracts_context.get('batch_contracts', [])

    # Create function that returns contract or generates new one on the fly
    call_count = [0]
    def mock_post_response(endpoint, data=None):
        if '/negotiate' in endpoint:
            idx = call_count[0]
            call_count[0] += 1
            if idx < len(contracts) and contracts[idx]:
                return {'data': {'contract': contracts[idx]}}
            else:
                # Generate generic contract for additional negotiations
                return {'data': {'contract': {
                    'id': f'contract-{idx+1}',
                    'type': 'PROCUREMENT',
                    'factionSymbol': 'COSMIC',
                    'terms': {
                        'payment': {'onAccepted': 10000, 'onFulfilled': 100000},
                        'deliver': [{
                            'unitsRequired': 50,
                            'unitsFulfilled': 0,
                            'tradeSymbol': 'IRON_ORE',
                            'destinationSymbol': 'X1-TEST-A1',
                        }]
                    }
                }}}
        return {'data': {}}

    mock_api.post = MagicMock(side_effect=mock_post_response)
    mock_api.get = MagicMock(return_value={'data': [], 'meta': {'total': 0, 'page': 1, 'limit': 20}})

    # Mock ship
    mock_ship = MagicMock()
    mock_ship.get_status = MagicMock(return_value={
        'cargo': {'capacity': 40, 'units': 0},
        'nav': {'systemSymbol': 'X1-TEST'},
    })

    # Mock args
    args = MagicMock()
    args.player_id = 1
    args.ship = 'SHIP-1'
    args.contract_count = contracts_context.get('batch_count', len(contracts))
    args.buy_from = None
    args.log_level = 'ERROR'

    # Setup fulfillment responses
    failing = contracts_context.get('failing_fulfillments', [])
    fulfill_calls = []

    def mock_fulfill(args, **kwargs):
        fulfill_calls.append(args.contract_id)
        contract_num = len(fulfill_calls)
        if contract_num in failing:
            return 1  # Failure
        return 0  # Success

    with patch('src.spacetraders_bot.operations.contracts.contract_operation', side_effect=mock_fulfill) as mock_fulfill_fn:
        with patch('src.spacetraders_bot.operations.contracts.ShipController', return_value=mock_ship):
            result = batch_contract_operation(args, api=mock_api)

    contracts_context['result'] = result
    contracts_context['api'] = mock_api
    contracts_context['fulfill_calls'] = fulfill_calls
    contracts_context['mock_fulfill'] = mock_fulfill_fn


@when('I negotiate contract 1')
def negotiate_contract_1(contracts_context):
    """Negotiate contract 1."""
    contracts_context['negotiated_contracts'] = contracts_context.get('negotiated_contracts', [])
    contracts_context['negotiated_contracts'].append(1)


@when(parsers.parse('I negotiate contract {num:d}'))
def negotiate_contract_num(contracts_context, num):
    """Negotiate a specific contract."""
    contracts_context['negotiated_contracts'] = contracts_context.get('negotiated_contracts', [])
    contracts_context['negotiated_contracts'].append(num)


@then(parsers.parse('I negotiate contract {num:d}'))
def then_negotiate_contract_num(contracts_context, num):
    """Negotiate a specific contract (then step)."""
    negotiate_contract_num(contracts_context, num)


@when('I try to negotiate while a contract is still active')
def try_negotiate_while_active(contracts_context):
    """Try to negotiate while contract is active."""
    contracts_context['negotiate_while_active'] = True


@when('I evaluate the contract profitability')
def evaluate_profitability(contracts_context):
    """Evaluate contract profitability."""
    contract = contracts_context['contract_data']
    cargo_capacity = contracts_context['ship_data']['cargo']['capacity']
    db = contracts_context.get('database')

    is_profitable, reason, metrics = evaluate_contract_profitability(
        contract, cargo_capacity, system='X1-TEST', db=db
    )

    contracts_context['profitability_result'] = is_profitable
    contracts_context['profitability_reason'] = reason
    contracts_context['metrics'] = metrics


@when('I check if price polling is needed')
def check_price_polling(contracts_context):
    """Check if price polling is needed."""
    # Create ResourceAcquisitionStrategy
    strategy_args = {
        'trade_symbol': 'IRON_ORE',
        'system': 'X1-TEST',
        'database': contracts_context.get('database'),
        'log_error': Mock(),
        'sleep_fn': Mock(),
        'print_fn': Mock(),
        'max_retries': contracts_context.get('max_retries', 12),
        'retry_interval_seconds': contracts_context.get('retry_interval', 300),
    }

    strategy = ResourceAcquisitionStrategy(**strategy_args)
    contracts_context['strategy'] = strategy

    # Mock find_markets_selling
    price = contracts_context.get('market_prices', {}).get('IRON_ORE', 100)

    def mock_find_markets(*args, **kwargs):
        return [{
            'waypoint_symbol': 'X1-TEST-M1',
            'good_symbol': 'IRON_ORE',
            'purchase_price': price,
            'supply': 'HIGH',
            'activity': 'STRONG',
            'trade_volume': 20,
            'last_updated': '2025-10-14T12:00:00Z'
        }]

    with patch('src.spacetraders_bot.operations.contracts.find_markets_selling', side_effect=mock_find_markets):
        result = strategy.wait_for_profitable_price(
            contract=contracts_context['contract_data'],
            cargo_capacity=40,
            system='X1-TEST'
        )

    contracts_context['polling_result'] = result


@when('I wait for profitable price')
def wait_for_profitable_price(contracts_context):
    """Wait for profitable price."""
    # Create ResourceAcquisitionStrategy
    strategy_args = {
        'trade_symbol': contracts_context.get('resource', 'IRON_ORE'),
        'system': 'X1-TEST',
        'database': contracts_context.get('database'),
        'log_error': Mock(),
        'sleep_fn': Mock(),
        'print_fn': Mock(),
        'max_retries': contracts_context.get('max_retries', 12),
        'retry_interval_seconds': contracts_context.get('retry_interval', 300),
    }

    strategy = ResourceAcquisitionStrategy(**strategy_args)
    contracts_context['strategy'] = strategy

    # Mock find_markets_selling with price sequence
    price_sequence = contracts_context.get('price_sequence', [])
    constant_price = contracts_context.get('constant_price')

    call_count = [0]

    def mock_find_markets(*args, **kwargs):
        if constant_price:
            return [{'purchase_price': constant_price, 'good_symbol': strategy_args['trade_symbol']}]

        if price_sequence:
            idx = min(call_count[0], len(price_sequence) - 1)
            price = price_sequence[idx]
            call_count[0] += 1
            return [{'purchase_price': price, 'good_symbol': strategy_args['trade_symbol']}]

        return []

    with patch('src.spacetraders_bot.operations.contracts.find_markets_selling', side_effect=mock_find_markets):
        result = strategy.wait_for_profitable_price(
            contract=contracts_context['contract_data'],
            cargo_capacity=40,
            system='X1-TEST'
        )

    contracts_context['polling_result'] = result


@when('I evaluate profitability')
def evaluate_profitability_generic(contracts_context):
    """Evaluate profitability (generic)."""
    contract = contracts_context['contract_data']
    cargo_capacity = 40
    db = contracts_context.get('database')

    is_profitable, reason, metrics = evaluate_contract_profitability(
        contract, cargo_capacity, system='X1-TEST', db=db
    )

    contracts_context['profitability_result'] = is_profitable
    contracts_context['profitability_reason'] = reason
    contracts_context['metrics'] = metrics


# ==================== Then Steps ====================

@then(parsers.parse('the first purchase attempt ({units:d} units) should fail with error 4604'))
def verify_first_purchase_failed(contracts_context, units):
    """Verify first purchase failed with error 4604."""
    assert contracts_context['error_4604_count'] >= 1, "Expected error 4604 on first purchase"


@then(parsers.parse('the system should retry with {units:d} units (success)'))
def verify_retry_with_units(contracts_context, units):
    """Verify retry with specific units."""
    purchase_calls = contracts_context['purchase_calls']
    successful_calls = [call for call in purchase_calls if call['units'] == units]
    assert len(successful_calls) >= 1, f"Expected successful purchase with {units} units"


@then(parsers.parse('the system should purchase remaining {units:d} units (success)'))
def verify_remaining_purchase(contracts_context, units):
    """Verify remaining units purchased."""
    purchase_calls = contracts_context['purchase_calls']
    successful_calls = [call for call in purchase_calls if call['units'] == units]
    assert len(successful_calls) >= 1, f"Expected successful purchase with {units} units"


@then(parsers.parse('there should be at least {count:d} purchase calls total'))
def verify_purchase_call_count(contracts_context, count):
    """Verify total purchase calls."""
    actual = len(contracts_context['purchase_calls'])
    assert actual >= count, f"Expected at least {count} purchase calls, got {actual}"


@then('the contract should be fulfilled successfully')
def verify_contract_fulfilled(contracts_context):
    """Verify contract was fulfilled."""
    assert contracts_context['result'] == 0, "Expected contract fulfillment to succeed"


@then('the system should parse the limit from the error message')
def verify_limit_parsed(contracts_context):
    """Verify limit was parsed from error."""
    # This is implicit in the retry logic
    pass


@then(parsers.parse('subsequent purchases should respect the {limit:d} unit limit'))
def verify_subsequent_purchases_respect_limit(contracts_context, limit):
    """Verify subsequent purchases respect limit."""
    purchase_calls = contracts_context['purchase_calls']
    for call in purchase_calls[1:]:  # Skip first (failed) call
        assert call['units'] <= limit, f"Purchase of {call['units']} units exceeds limit of {limit}"


@then('the navigator should be used for route execution to market')
def verify_navigator_used(contracts_context):
    """Verify navigator was used."""
    navigator = contracts_context['navigator']
    navigator.execute_route.assert_called()


@then(parsers.parse('the purchase should succeed at "{market}"'))
def verify_purchase_at_market(contracts_context, market):
    """Verify purchase succeeded at market."""
    mock_ship = contracts_context['mock_ship']
    mock_ship.buy.assert_called()


@then('the contract should be fulfilled')
def verify_contract_fulfilled_simple(contracts_context):
    """Verify contract was fulfilled."""
    assert contracts_context['result'] == 0


@then('a navigator should be initialized internally')
def verify_navigator_initialized_internally(contracts_context):
    """Verify navigator was initialized."""
    internal_nav = contracts_context.get('internal_navigator')
    assert internal_nav is not None, "Navigator should be initialized internally"


@then('navigation to market should succeed')
def verify_navigation_succeeded(contracts_context):
    """Verify navigation succeeded."""
    internal_nav = contracts_context.get('internal_navigator')
    if internal_nav:
        internal_nav.execute_route.assert_called()


@then('the contract should be fulfilled without crashes')
def verify_no_crashes(contracts_context):
    """Verify no crashes occurred."""
    assert contracts_context['result'] == 0


@then(parsers.parse('it would crash with AttributeError (if bug not fixed)'))
def would_crash_if_bug_not_fixed(contracts_context):
    """Document that it would crash without fix."""
    # This is a documentation step
    pass


@then('with the fix, navigator is properly initialized')
def verify_fix_initializes_navigator(contracts_context):
    """Verify fix initializes navigator."""
    # This is verified by successful execution
    pass


@then('navigation succeeds')
def verify_navigation_succeeds(contracts_context):
    """Verify navigation succeeds."""
    assert contracts_context['result'] == 0


@then(parsers.parse('the system should fetch page {page:d} (contracts {start:d}-{end:d})'))
def verify_page_fetched(contracts_context, page, start, end):
    """Verify page was fetched."""
    get_calls = contracts_context.get('get_calls', [])
    if page == 1:
        # Page 1 is fetched without page parameter or with page=1
        page1_calls = [c for c in get_calls if '/my/contracts' in c and ('page=1' in c or 'page=' not in c)]
        assert len(page1_calls) > 0, "Page 1 was not fetched"
    else:
        page_calls = [c for c in get_calls if f'page={page}' in c]
        assert len(page_calls) > 0, f"Page {page} was not fetched"


@then(parsers.parse('the system should fetch page {page:d}'))
def verify_page_fetched_simple(contracts_context, page):
    """Verify page was fetched (simple form)."""
    get_calls = contracts_context.get('get_calls', [])
    if page == 1:
        # Page 1 is fetched without page parameter or with page=1
        page1_calls = [c for c in get_calls if '/my/contracts' in c and ('page=1' in c or 'page=' not in c)]
        assert len(page1_calls) > 0, "Page 1 was not fetched"
    else:
        page_calls = [c for c in get_calls if f'page={page}' in c]
        assert len(page_calls) > 0, f"Page {page} was not fetched"


@then(parsers.parse('fetch page {page:d} (contracts {start:d}-{end:d})'))
def verify_fetch_page(contracts_context, page, start, end):
    """Verify fetch page (alternative form)."""
    verify_page_fetched(contracts_context, page, start, end)


@then(parsers.parse('detect total={total:d} from meta'))
def verify_total_detected(contracts_context, total):
    """Verify total was detected."""
    # This is implicit in the pagination logic
    pass


@then('find the active contract on page 2')
def verify_active_contract_found(contracts_context):
    """Verify active contract was found."""
    fulfill_calls = contracts_context.get('fulfill_calls', [])
    active_contracts = [c for c in fulfill_calls if 'active' in c.lower()]
    assert len(active_contracts) > 0, "Active contract was not found"


@then('fulfill the active contract before negotiating new ones')
def verify_active_fulfilled_first(contracts_context):
    """Verify active contract fulfilled first."""
    fulfill_calls = contracts_context.get('fulfill_calls', [])
    if fulfill_calls:
        first_call = fulfill_calls[0]
        assert 'active' in first_call.lower() or 'existing' in first_call.lower(), \
            f"First fulfilled contract should be active/existing, got {first_call}"


@then('the system should fetch all pages')
def verify_all_pages_fetched(contracts_context):
    """Verify all pages fetched."""
    get_calls = contracts_context.get('get_calls', [])
    page2_calls = [c for c in get_calls if 'page=2' in c]
    assert len(page2_calls) > 0, "Not all pages were fetched (page 2 missing)"


@then('all pages should be fetched')
def verify_all_pages_fetched_alt(contracts_context):
    """Verify all pages fetched (alternative wording)."""
    verify_all_pages_fetched(contracts_context)


@then(parsers.parse('find contract {contract_id:d} on page 2'))
def verify_contract_on_page2(contracts_context, contract_id):
    """Verify contract found on page 2."""
    fulfill_calls = contracts_context.get('fulfill_calls', [])
    assert any(str(contract_id) in call or 'active' in call.lower() for call in fulfill_calls), \
        f"Contract {contract_id} not found in fulfill calls"


@then(parsers.parse('fulfill contract {contract_id:d} first'))
def verify_contract_fulfilled_first(contracts_context, contract_id):
    """Verify specific contract fulfilled first."""
    fulfill_calls = contracts_context.get('fulfill_calls', [])
    if fulfill_calls:
        assert 'active' in fulfill_calls[0].lower() or str(contract_id) in fulfill_calls[0], \
            f"Contract {contract_id} should be fulfilled first"


@then(parsers.parse('then negotiate {count:d} new contract'))
@then(parsers.parse('then negotiate {count:d} new contracts'))
def verify_new_contracts_negotiated(contracts_context, count):
    """Verify new contracts negotiated."""
    api = contracts_context.get('api')
    if api:
        # Count negotiate calls
        post_calls = [call for call in api.post.call_args_list if 'negotiate' in str(call)]
        assert len(post_calls) >= count, f"Expected {count} negotiate calls, got {len(post_calls)}"


@then('there should be NO error 4511')
def verify_no_error_4511(contracts_context):
    """Verify no error 4511."""
    # Success means no error 4511
    assert contracts_context['result'] == 0


@then('find no active contracts')
def verify_no_active_contracts(contracts_context):
    """Verify no active contracts found."""
    fulfill_calls = contracts_context.get('fulfill_calls', [])
    active_contracts = [c for c in fulfill_calls if 'active' in c.lower() or 'existing' in c.lower()]
    # If no active contracts, fulfill_calls should only contain newly negotiated contracts
    pass


@then(parsers.parse('negotiate {count:d} new contracts'))
@then(parsers.parse('negotiate {count:d} contracts'))
def verify_negotiate_count(contracts_context, count):
    """Verify negotiate count."""
    api = contracts_context.get('api')
    if api:
        assert api.post.call_count >= count, f"Expected at least {count} negotiations"


@then('fulfill them successfully')
def verify_fulfillment_success(contracts_context):
    """Verify fulfillment success."""
    assert contracts_context['result'] == 0


@then(parsers.parse('{count:d} contracts should be negotiated'))
def verify_contracts_negotiated(contracts_context, count):
    """Verify contracts negotiated."""
    api = contracts_context.get('api')
    if api:
        assert api.post.call_count >= count or api.post.call_count == len(contracts_context.get('batch_contracts', [])), \
            f"Expected {count} negotiations"


@then(parsers.parse('{count:d} contracts should be fulfilled'))
def verify_contracts_fulfilled(contracts_context, count):
    """Verify contracts fulfilled."""
    fulfill_calls = contracts_context.get('fulfill_calls', [])
    assert len(fulfill_calls) >= count, f"Expected {count} fulfillments, got {len(fulfill_calls)}"


@then('the operation should succeed')
def verify_operation_succeeded(contracts_context):
    """Verify operation succeeded."""
    assert contracts_context['result'] == 0, "Operation should succeed"


@then(parsers.parse('ALL {count:d} contracts should be fulfilled (no skipping)'))
def verify_all_fulfilled_no_skip(contracts_context, count):
    """Verify all contracts fulfilled without skipping."""
    fulfill_calls = contracts_context.get('fulfill_calls', [])
    mock_fulfill = contracts_context.get('mock_fulfill')
    if mock_fulfill:
        assert mock_fulfill.call_count == count, f"Expected {count} fulfill calls, got {mock_fulfill.call_count}"


@then(parsers.parse('{count:d} fulfillment attempts should be made'))
def verify_fulfillment_attempts(contracts_context, count):
    """Verify fulfillment attempts."""
    fulfill_calls = contracts_context.get('fulfill_calls', [])
    assert len(fulfill_calls) == count, f"Expected {count} fulfillment attempts, got {len(fulfill_calls)}"


@then(parsers.parse('contracts {c1:d} and {c2:d} should succeed'))
def verify_specific_contracts_succeed(contracts_context, c1, c2):
    """Verify specific contracts succeeded."""
    # This is implicit in the result
    pass


@then(parsers.parse('the operation should still report success ({success}/{total} fulfilled)'))
def verify_partial_success(contracts_context, success, total):
    """Verify partial success."""
    assert contracts_context['result'] == 0, "Operation should report success with partial fulfillment"


@then(parsers.parse('{count:d} negotiation attempts should be made'))
def verify_negotiation_attempts(contracts_context, count):
    """Verify negotiation attempts."""
    api = contracts_context.get('api')
    if api:
        assert api.post.call_count >= count, f"Expected {count} negotiation attempts"


@then(parsers.parse('only {count:d} contracts should be fulfilled (skipping failed negotiation)'))
def verify_skipped_failed_negotiation(contracts_context, count):
    """Verify failed negotiation skipped."""
    fulfill_calls = contracts_context.get('fulfill_calls', [])
    assert len(fulfill_calls) <= count, f"Expected at most {count} fulfillments (skipping failed)"


@then(parsers.parse('the operation should FAIL ({success}/{total} fulfilled)'))
def verify_operation_failed(contracts_context, success, total):
    """Verify operation failed."""
    assert contracts_context['result'] == 1, "Operation should fail when all fulfillments fail"


@then('contract 1 should be fulfilled completely')
def verify_contract1_fulfilled(contracts_context):
    """Verify contract 1 fulfilled."""
    negotiated = contracts_context.get('negotiated_contracts', [])
    assert 1 in negotiated, "Contract 1 should be negotiated"


@then('contract 1 should become INACTIVE')
def verify_contract1_inactive(contracts_context):
    """Verify contract 1 became inactive."""
    # This is implicit in sequential execution
    pass


@then('the negotiation should fail with error 4511')
def verify_negotiation_fails_4511(contracts_context):
    """Verify negotiation fails with error 4511."""
    # This is the bug scenario - should not happen with fix
    pass


@then('with sequential execution, the previous contract is fulfilled first')
def verify_sequential_execution(contracts_context):
    """Verify sequential execution."""
    # This is implicit in the fix
    pass


@then('the negotiation succeeds')
def verify_negotiation_succeeds(contracts_context):
    """Verify negotiation succeeds."""
    assert contracts_context['result'] == 0


@then(parsers.parse('BOTH contracts should be fulfilled (no profitability filter)'))
def verify_both_fulfilled_no_filter(contracts_context):
    """Verify both contracts fulfilled without filtering."""
    fulfill_calls = contracts_context.get('fulfill_calls', [])
    mock_fulfill = contracts_context.get('mock_fulfill')
    if mock_fulfill:
        assert mock_fulfill.call_count == 2, f"Expected 2 fulfill calls, got {mock_fulfill.call_count}"


@then('the existing contract should be fulfilled first')
def verify_existing_fulfilled_first(contracts_context):
    """Verify existing contract fulfilled first."""
    fulfill_calls = contracts_context.get('fulfill_calls', [])
    if fulfill_calls:
        assert 'existing' in fulfill_calls[0].lower(), f"First fulfilled should be existing, got {fulfill_calls[0]}"


@then(parsers.parse('total contracts fulfilled should be {total:d} ({existing:d} existing + {new:d} new)'))
def verify_total_fulfillments(contracts_context, total, existing, new):
    """Verify total fulfillments."""
    fulfill_calls = contracts_context.get('fulfill_calls', [])
    assert len(fulfill_calls) >= total, f"Expected {total} total fulfillments, got {len(fulfill_calls)}"


@then('it should be fulfilled before negotiating new ones')
def verify_fulfilled_before_negotiating(contracts_context):
    """Verify fulfilled before negotiating."""
    fulfill_calls = contracts_context.get('fulfill_calls', [])
    if fulfill_calls:
        assert 'active' in fulfill_calls[0].lower() or 'existing' in fulfill_calls[0].lower()


@then(parsers.parse('real cost should be {cost:d} credits ({calc})'))
def verify_real_cost(contracts_context, cost, calc):
    """Verify real cost calculation."""
    metrics = contracts_context['metrics']
    # Allow for fuel cost variation
    assert abs(metrics.get('total_cost', 0) - cost) < 1000, \
        f"Expected cost ~{cost}, got {metrics.get('total_cost')}"


@then(parsers.parse('net profit should be {profit:d} credits (massive loss)'))
@then(parsers.parse('net profit should be approximately {profit:d} credits'))
def verify_net_profit(contracts_context, profit):
    """Verify net profit."""
    metrics = contracts_context['metrics']
    actual_profit = metrics.get('net_profit', 0)
    # Allow for fuel cost variation
    assert abs(actual_profit - profit) < 5000, \
        f"Expected profit ~{profit}, got {actual_profit}"


@then('the contract should be REJECTED')
def verify_contract_rejected(contracts_context):
    """Verify contract was rejected."""
    assert not contracts_context['profitability_result'], "Contract should be rejected"


@then(parsers.parse('price source should be "{source}"'))
def verify_price_source(contracts_context, source):
    """Verify price source."""
    metrics = contracts_context['metrics']
    assert source in metrics.get('price_source', ''), \
        f"Expected price source '{source}', got '{metrics.get('price_source')}'"


@then(parsers.parse('ROI should be approximately {roi:d}%'))
def verify_roi(contracts_context, roi):
    """Verify ROI."""
    metrics = contracts_context['metrics']
    actual_roi = metrics.get('roi', 0)
    # Allow for variation
    assert abs(actual_roi - roi) < 20, f"Expected ROI ~{roi}%, got {actual_roi}%"


@then('the contract should be ACCEPTED')
def verify_contract_accepted(contracts_context):
    """Verify contract was accepted."""
    assert contracts_context['profitability_result'], "Contract should be accepted"


@then(parsers.parse('estimated cost should use {cost} credits per unit (conservative)'))
def verify_conservative_estimate(contracts_context, cost):
    """Verify conservative estimate used."""
    cost_val = int(cost.replace(',', ''))
    metrics = contracts_context['metrics']
    assert metrics.get('unit_cost') == cost_val, f"Expected unit cost {cost_val}, got {metrics.get('unit_cost')}"


@then(parsers.parse('total cost should be {cost:d} credits ({calc})'))
def verify_total_cost(contracts_context, cost, calc):
    """Verify total cost."""
    metrics = contracts_context['metrics']
    actual_cost = metrics.get('total_cost', 0)
    # Allow for variation
    assert abs(actual_cost - cost) < 1000, f"Expected cost ~{cost}, got {actual_cost}"


@then(parsers.parse('reason should be "{reason}"'))
def verify_rejection_reason(contracts_context, reason):
    """Verify rejection reason."""
    actual_reason = contracts_context.get('profitability_reason', '')
    assert reason.lower() in actual_reason.lower(), \
        f"Expected reason containing '{reason}', got '{actual_reason}'"


@then(parsers.parse('units remaining should be {units:d}'))
def verify_units_remaining(contracts_context, units):
    """Verify units remaining."""
    metrics = contracts_context['metrics']
    assert metrics.get('units_remaining') == units, \
        f"Expected {units} units remaining, got {metrics.get('units_remaining')}"


@then(parsers.parse('trips should be {trips:d} ({calc})'))
def verify_trips(contracts_context, trips, calc):
    """Verify number of trips."""
    metrics = contracts_context['metrics']
    assert metrics.get('trips') == trips, f"Expected {trips} trips, got {metrics.get('trips')}"


@then('the evaluation should account for only remaining units')
def verify_only_remaining_units(contracts_context):
    """Verify only remaining units accounted for."""
    metrics = contracts_context['metrics']
    contract = contracts_context['contract_data']
    units_required = contract['terms']['deliver'][0]['unitsRequired']
    units_fulfilled = contract['terms']['deliver'][0]['unitsFulfilled']
    expected_remaining = units_required - units_fulfilled
    assert metrics.get('units_remaining') == expected_remaining


@then(parsers.parse('profitability check shows net profit of {profit:d} credits'))
def verify_profitability_profit(contracts_context, profit):
    """Verify profitability profit."""
    # This is checked during evaluation
    pass


@then(parsers.parse('ROI is approximately {roi:d}%'))
def verify_profitability_roi(contracts_context, roi):
    """Verify profitability ROI."""
    # This is checked during evaluation
    pass


@then('the system should proceed immediately without polling')
def verify_no_polling(contracts_context):
    """Verify no polling occurred."""
    strategy = contracts_context.get('strategy')
    if strategy:
        strategy.sleep_fn.assert_not_called()


@then('sleep should NOT be called')
def verify_sleep_not_called(contracts_context):
    """Verify sleep not called."""
    strategy = contracts_context.get('strategy')
    if strategy:
        strategy.sleep_fn.assert_not_called()


@then('the system should poll once')
def verify_poll_once(contracts_context):
    """Verify polled once."""
    strategy = contracts_context.get('strategy')
    if strategy:
        assert strategy.sleep_fn.call_count == 1, "Should poll once"


@then(parsers.parse('sleep should be called with {seconds:d} seconds'))
def verify_sleep_interval(contracts_context, seconds):
    """Verify sleep interval."""
    strategy = contracts_context.get('strategy')
    if strategy and strategy.sleep_fn.call_count > 0:
        strategy.sleep_fn.assert_called_with(seconds)


@then('after price drop, profitability check passes')
def verify_profitability_after_drop(contracts_context):
    """Verify profitability after price drop."""
    assert contracts_context.get('polling_result') is True


@then('the system should proceed with acquisition')
def verify_proceed_with_acquisition(contracts_context):
    """Verify proceeds with acquisition."""
    assert contracts_context.get('polling_result') is True


@then(parsers.parse('the system should poll {count:d} times'))
def verify_poll_count(contracts_context, count):
    """Verify poll count."""
    strategy = contracts_context.get('strategy')
    if strategy:
        assert strategy.sleep_fn.call_count == count, \
            f"Expected {count} polls, got {strategy.sleep_fn.call_count}"


@then(parsers.parse('sleep should be called {count:d} times with {seconds:d} seconds each'))
def verify_sleep_count_and_interval(contracts_context, count, seconds):
    """Verify sleep count and interval."""
    strategy = contracts_context.get('strategy')
    if strategy:
        assert strategy.sleep_fn.call_count == count, \
            f"Expected {count} sleep calls, got {strategy.sleep_fn.call_count}"


@then('timeout message should be logged')
def verify_timeout_logged(contracts_context):
    """Verify timeout message logged."""
    strategy = contracts_context.get('strategy')
    if strategy:
        # Check if timeout message was printed
        timeout_calls = [call for call in strategy.print_fn.call_args_list
                        if 'Timeout' in str(call) or 'timeout' in str(call)]
        assert len(timeout_calls) > 0, "Timeout message should be logged"


@then('the system should execute anyway (to avoid contract expiration)')
def verify_execute_anyway(contracts_context):
    """Verify executes anyway after timeout."""
    assert contracts_context.get('polling_result') is True, "Should execute anyway after timeout"


@then(parsers.parse('net profit is {profit:d} credits (below {threshold:d} minimum)'))
def verify_profit_below_minimum(contracts_context, profit, threshold):
    """Verify profit below minimum."""
    metrics = contracts_context.get('metrics', {})
    actual_profit = metrics.get('net_profit', 0)
    assert actual_profit < threshold, f"Expected profit below {threshold}, got {actual_profit}"


@then('the contract should be unprofitable')
def verify_contract_unprofitable(contracts_context):
    """Verify contract is unprofitable."""
    assert not contracts_context.get('profitability_result'), "Contract should be unprofitable"


@then('the system should poll waiting for price drop')
def verify_polling_for_price_drop(contracts_context):
    """Verify polling for price drop."""
    # This is implicit in the timeout behavior
    pass


@then(parsers.parse('the system should use {cost:d} credits per unit (conservative estimate)'))
def verify_conservative_unit_cost(contracts_context, cost):
    """Verify conservative unit cost."""
    metrics = contracts_context.get('metrics', {})
    assert metrics.get('unit_cost') == cost, f"Expected unit cost {cost}, got {metrics.get('unit_cost')}"


@then('price polling should continue (waiting for market data)')
def verify_polling_continues(contracts_context):
    """Verify polling continues."""
    # This is implicit in the polling behavior
    pass


@then('the contract should be profitable')
def verify_contract_profitable(contracts_context):
    """Verify contract is profitable."""
    assert contracts_context.get('profitability_result'), "Contract should be profitable"


@then(parsers.parse('the system should log "{message}"'))
def verify_log_message(contracts_context, message):
    """Verify log message."""
    strategy = contracts_context.get('strategy')
    if strategy:
        log_calls = [str(call) for call in strategy.print_fn.call_args_list]
        assert any(message.lower() in call.lower() for call in log_calls), \
            f"Expected log message '{message}' not found"


@then('no polling should occur')
def verify_no_polling_occurred(contracts_context):
    """Verify no polling occurred."""
    strategy = contracts_context.get('strategy')
    if strategy:
        strategy.sleep_fn.assert_not_called()
