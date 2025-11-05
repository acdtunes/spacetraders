"""Step definitions for simplified batch contract workflow"""
import pytest
import asyncio
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, AsyncMock
from datetime import datetime, timezone, timedelta

from application.contracts.commands.batch_contract_workflow import (
    BatchContractWorkflowCommand,
    BatchContractWorkflowHandler,
    BatchResult
)
from domain.shared.contract import Contract, ContractTerms, Delivery, Payment
from domain.shared.value_objects import Waypoint

# Mark all tests in this file as integration tests (make real API calls, slow)
pytestmark = pytest.mark.integration

# Load all scenarios from the feature file
scenarios('../../../features/application/contracts/batch_workflow_simple.feature')


@pytest.fixture
def context():
    """Provide shared context dictionary for test data"""
    return {
        'contract_counter': 0,
        'contracts': []
    }


@given(parsers.parse('a player with agent "{agent_symbol}"'))
def player_with_agent(context, agent_symbol):
    """Register test player"""
    context['agent_symbol'] = agent_symbol
    context['player_id'] = 1


@given(parsers.parse('a ship "{ship_symbol}" in system "{system}"'))
def ship_in_system(context, ship_symbol, system):
    """Set up ship"""
    context['ship_symbol'] = ship_symbol
    context['system'] = system


@given('contract negotiation returns a profitable contract')
def mock_profitable_contract(context):
    """Mock contract negotiation to return profitable contract"""
    contract = Contract(
        contract_id="TEST-CONTRACT-1",
        faction_symbol="COSMIC",
        type="PROCUREMENT",
        terms=ContractTerms(
            deadline=datetime.now(timezone.utc) + timedelta(days=7),
            payment=Payment(on_accepted=10000, on_fulfilled=15000),
            deliveries=[
                Delivery(
                    trade_symbol="IRON_ORE",
                    destination=Waypoint(symbol="X1-TEST-DEST", x=0.0, y=0.0),
                    units_required=50,
                    units_fulfilled=0
                )
            ]
        ),
        accepted=False,
        fulfilled=False,
        deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
    )
    context['contracts'] = [contract]
    context['contract_is_profitable'] = True


@given('contract negotiation returns an unprofitable contract')
def mock_unprofitable_contract(context):
    """Mock contract negotiation to return unprofitable contract"""
    contract = Contract(
        contract_id="TEST-CONTRACT-1",
        faction_symbol="COSMIC",
        type="PROCUREMENT",
        terms=ContractTerms(
            deadline=datetime.now(timezone.utc) + timedelta(days=7),
            payment=Payment(on_accepted=100, on_fulfilled=200),
            deliveries=[
                Delivery(
                    trade_symbol="IRON_ORE",
                    destination=Waypoint(symbol="X1-TEST-DEST", x=0.0, y=0.0),
                    units_required=1000,
                    units_fulfilled=0
                )
            ]
        ),
        accepted=False,
        fulfilled=False,
        deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
    )
    context['contracts'] = [contract]
    context['contract_is_profitable'] = False


@given('market data is available for required goods')
def mock_market_data(context):
    """Mock market data availability"""
    context['has_market_data'] = True


@given('ship has sufficient cargo capacity')
def mock_cargo_capacity(context):
    """Mock cargo capacity"""
    context['cargo_capacity'] = 100


@given(parsers.parse('contract negotiation returns a contract requiring {units:d} units'))
def mock_contract_with_units(context, units):
    """Mock contract with specific units"""
    contract = Contract(
        contract_id="TEST-CONTRACT-LARGE",
        faction_symbol="COSMIC",
        type="PROCUREMENT",
        terms=ContractTerms(
            deadline=datetime.now(timezone.utc) + timedelta(days=7),
            payment=Payment(on_accepted=10000, on_fulfilled=15000),
            deliveries=[
                Delivery(
                    trade_symbol="LIQUID_HYDROGEN",
                    destination=Waypoint(symbol="X1-TEST-DEST", x=0.0, y=0.0),
                    units_required=units,
                    units_fulfilled=0
                )
            ]
        ),
        accepted=False,
        fulfilled=False,
        deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
    )
    context['contracts'] = [contract]
    context['contract_is_profitable'] = True


@given(parsers.parse('the ship has cargo capacity of {capacity:d} units'))
def set_ship_cargo_capacity(context, capacity):
    """Set ship cargo capacity"""
    context['cargo_capacity'] = capacity


@given('contract negotiation returns profitable contracts')
def mock_multiple_profitable_contracts(context):
    """Mock multiple profitable contracts"""
    contracts = []
    for i in range(5):
        contract = Contract(
            contract_id=f"TEST-CONTRACT-{i+1}",
            faction_symbol="COSMIC",
            type="PROCUREMENT",
            terms=ContractTerms(
                deadline=datetime.now(timezone.utc) + timedelta(days=7),
                payment=Payment(on_accepted=10000, on_fulfilled=15000),
                deliveries=[
                    Delivery(
                        trade_symbol="IRON_ORE",
                        destination=Waypoint(symbol="X1-TEST-DEST", x=0.0, y=0.0),
                        units_required=50,
                        units_fulfilled=0
                    )
                ]
            ),
            accepted=False,
            fulfilled=False,
            deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
        )
        contracts.append(contract)
    context['contracts'] = contracts
    context['contract_is_profitable'] = True


@given(parsers.parse('contract {num:d} will succeed'))
def contract_will_succeed(context, num):
    """Mark contract to succeed"""
    if 'contract_outcomes' not in context:
        context['contract_outcomes'] = {}
    context['contract_outcomes'][num] = 'success'

    # Ensure contracts list exists with enough entries
    if 'contracts' not in context:
        context['contracts'] = []

    # Add contract if needed
    while len(context['contracts']) < num:
        contract = Contract(
            contract_id=f"TEST-CONTRACT-{len(context['contracts'])+1}",
            faction_symbol="COSMIC",
            type="PROCUREMENT",
            terms=ContractTerms(
                deadline=datetime.now(timezone.utc) + timedelta(days=7),
                payment=Payment(on_accepted=10000, on_fulfilled=15000),
                deliveries=[
                    Delivery(
                        trade_symbol="IRON_ORE",
                        destination=Waypoint(symbol="X1-TEST-DEST", x=0.0, y=0.0),
                        units_required=50,
                        units_fulfilled=0
                    )
                ]
            ),
            accepted=False,
            fulfilled=False,
            deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
        )
        context['contracts'].append(contract)
    context['contract_is_profitable'] = True


@given(parsers.parse('contract {num:d} will fail during fulfillment'))
def contract_will_fail(context, num):
    """Mark contract to fail"""
    if 'contract_outcomes' not in context:
        context['contract_outcomes'] = {}
    context['contract_outcomes'][num] = 'failure'

    # Ensure contracts list exists with enough entries
    if 'contracts' not in context:
        context['contracts'] = []

    # Add contract if needed
    while len(context['contracts']) < num:
        contract = Contract(
            contract_id=f"TEST-CONTRACT-{len(context['contracts'])+1}",
            faction_symbol="COSMIC",
            type="PROCUREMENT",
            terms=ContractTerms(
                deadline=datetime.now(timezone.utc) + timedelta(days=7),
                payment=Payment(on_accepted=10000, on_fulfilled=15000),
                deliveries=[
                    Delivery(
                        trade_symbol="IRON_ORE",
                        destination=Waypoint(symbol="X1-TEST-DEST", x=0.0, y=0.0),
                        units_required=50,
                        units_fulfilled=0
                    )
                ]
            ),
            accepted=False,
            fulfilled=False,
            deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
        )
        context['contracts'].append(contract)
    context['contract_is_profitable'] = True


@given('an active contract already exists and is accepted')
def active_contract_exists(context):
    """Set up scenario where active contract exists"""
    contract = Contract(
        contract_id="EXISTING-CONTRACT-1",
        faction_symbol="COSMIC",
        type="PROCUREMENT",
        terms=ContractTerms(
            deadline=datetime.now(timezone.utc) + timedelta(days=7),
            payment=Payment(on_accepted=10000, on_fulfilled=15000),
            deliveries=[
                Delivery(
                    trade_symbol="IRON_ORE",
                    destination=Waypoint(symbol="X1-TEST-DEST", x=0.0, y=0.0),
                    units_required=50,
                    units_fulfilled=0
                )
            ]
        ),
        accepted=True,  # Already accepted!
        fulfilled=False,
        deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
    )
    context['existing_active_contract'] = contract
    context['contract_is_profitable'] = True


@when(parsers.parse('I execute batch workflow with {iterations:d} iteration'))
@when(parsers.parse('I execute batch workflow with {iterations:d} iterations'))
def execute_batch_workflow(context, iterations):
    """Execute batch workflow with mocked dependencies"""

    # Create mock mediator
    mock_mediator = Mock()

    # Mock negotiate contract
    def mock_negotiate(cmd):
        idx = context.get('contract_counter', 0)
        if idx < len(context.get('contracts', [])):
            contract = context['contracts'][idx]
            context['contract_counter'] = idx + 1
            return contract
        return None

    # Mock evaluate profitability
    from application.contracts.queries.evaluate_profitability import ProfitabilityResult
    def mock_evaluate(query):
        if context.get('contract_is_profitable', False):
            return ProfitabilityResult(
                is_profitable=True,
                net_profit=10000,
                purchase_cost=5000,
                trips_required=1,
                reason="Profitable"
            )
        else:
            return ProfitabilityResult(
                is_profitable=False,
                net_profit=-5000,
                purchase_cost=20000,
                trips_required=1,
                reason="Not profitable"
            )

    # Mock accept contract
    def mock_accept(cmd):
        return True

    # Mock purchase cargo
    def mock_purchase(cmd):
        return {'data': {'cargo': {'units': 50}}}

    # Mock deliver contract
    def mock_deliver(cmd):
        return True

    # Mock fulfill contract
    def mock_fulfill(cmd):
        contract_num = context.get('contract_counter', 1)
        outcome = context.get('contract_outcomes', {}).get(contract_num, 'success')
        if outcome == 'failure':
            raise Exception("Contract fulfillment failed")
        return True

    # Mock get active contracts
    def mock_get_active_contracts(query):
        existing_contract = context.get('existing_active_contract')
        if existing_contract:
            return [existing_contract]
        return []

    # Configure mock mediator to handle different command types
    def mock_send(cmd):
        from application.contracts.commands.negotiate_contract import NegotiateContractCommand
        from application.contracts.queries.evaluate_profitability import EvaluateContractProfitabilityQuery
        from application.contracts.queries.get_active_contracts import GetActiveContractsQuery
        from application.contracts.commands.accept_contract import AcceptContractCommand
        from application.trading.commands.purchase_cargo import PurchaseCargoCommand
        from application.contracts.commands.deliver_contract import DeliverContractCommand
        from application.contracts.commands.fulfill_contract import FulfillContractCommand

        if isinstance(cmd, GetActiveContractsQuery):
            return mock_get_active_contracts(cmd)
        elif isinstance(cmd, NegotiateContractCommand):
            return mock_negotiate(cmd)
        elif isinstance(cmd, EvaluateContractProfitabilityQuery):
            return mock_evaluate(cmd)
        elif isinstance(cmd, AcceptContractCommand):
            return mock_accept(cmd)
        elif isinstance(cmd, PurchaseCargoCommand):
            return mock_purchase(cmd)
        elif isinstance(cmd, DeliverContractCommand):
            return mock_deliver(cmd)
        elif isinstance(cmd, FulfillContractCommand):
            return mock_fulfill(cmd)
        return None

    mock_mediator.send_async = AsyncMock(side_effect=mock_send)

    # Mock ship repository
    mock_ship_repo = Mock()

    # Create mock ship with cargo capacity from context
    cargo_capacity = context.get('cargo_capacity', 100)
    mock_ship = Mock()
    mock_ship.cargo_capacity = cargo_capacity
    mock_ship_repo.find_by_symbol.return_value = mock_ship

    # Create handler with mocked mediator and ship repository
    handler = BatchContractWorkflowHandler(
        mediator=mock_mediator,
        ship_repository=mock_ship_repo
    )

    command = BatchContractWorkflowCommand(
        ship_symbol=context['ship_symbol'],
        iterations=iterations,
        player_id=context['player_id']
    )

    try:
        result = asyncio.run(handler.handle(command))
        context['workflow_result'] = result
        context['workflow_error'] = None
    except Exception as e:
        context['workflow_result'] = None
        context['workflow_error'] = e


@then(parsers.parse('{count:d} contract should be negotiated'))
@then(parsers.parse('{count:d} contracts should be negotiated'))
def verify_negotiated(context, count):
    """Verify contracts negotiated"""
    error = context.get('workflow_error')
    if error:
        raise AssertionError(f"Handler raised exception: {error}")
    assert context.get('workflow_result') is not None, "No workflow result"
    assert context['workflow_result'].negotiated == count, \
        f"Expected {count} negotiated, got {context['workflow_result'].negotiated}"


@then(parsers.parse('{count:d} contract should be accepted'))
@then(parsers.parse('{count:d} contracts should be accepted'))
def verify_accepted(context, count):
    """Verify contracts accepted"""
    assert context.get('workflow_result') is not None
    assert context['workflow_result'].accepted == count, \
        f"Expected {count} accepted, got {context['workflow_result'].accepted}"


@then(parsers.parse('{count:d} contract should be fulfilled'))
@then(parsers.parse('{count:d} contracts should be fulfilled'))
def verify_fulfilled(context, count):
    """Verify contracts fulfilled"""
    assert context.get('workflow_result') is not None
    assert context['workflow_result'].fulfilled == count, \
        f"Expected {count} fulfilled, got {context['workflow_result'].fulfilled}"


@then('the result should show positive profit')
def verify_positive_profit(context):
    """Verify positive profit"""
    assert context.get('workflow_result') is not None
    assert context['workflow_result'].total_profit > 0


@then('the result should show zero profit')
def verify_zero_profit(context):
    """Verify zero profit"""
    assert context.get('workflow_result') is not None
    assert context['workflow_result'].total_profit == 0


@then(parsers.parse('{count:d} contract should fail'))
@then(parsers.parse('{count:d} contracts should fail'))
def verify_failed(context, count):
    """Verify contracts failed"""
    assert context.get('workflow_result') is not None
    assert context['workflow_result'].failed == count, \
        f"Expected {count} failed, got {context['workflow_result'].failed}"


@then(parsers.parse('{count:d} trip should be required'))
@then(parsers.parse('{count:d} trips should be required'))
def verify_trips(context, count):
    """Verify trips required"""
    assert context.get('workflow_result') is not None
    assert context['workflow_result'].total_trips == count, \
        f"Expected {count} trips, got {context['workflow_result'].total_trips}"
