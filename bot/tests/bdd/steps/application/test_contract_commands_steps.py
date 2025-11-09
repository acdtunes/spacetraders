"""BDD step definitions for Contract Commands"""
import asyncio
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from datetime import datetime, timezone, timedelta
from unittest.mock import Mock

from domain.shared.contract import (
    Contract,
    ContractTerms,
    Delivery,
    Payment
)
from domain.shared.value_objects import Waypoint
from domain.shared.player import Player
from application.contracts.commands.accept_contract import AcceptContractCommand
from application.contracts.commands.deliver_contract import DeliverContractCommand
from application.contracts.commands.fulfill_contract import FulfillContractCommand
from application.contracts.commands.negotiate_contract import NegotiateContractCommand
from configuration.container import (
    get_mediator,
    get_player_repository,
    get_contract_repository,
    reset_container,
    get_engine
)

# Load scenarios
scenarios('../../features/application/contract_commands.feature')


@pytest.fixture
def mediator(context):
    """Get mediator instance with SQLAlchemy and mocked API"""
    import configuration.container as container_module

    # Mock the API client factory
    mock_api_factory = Mock(return_value=context.get('mock_api'))
    container_module.get_api_client_for_player = mock_api_factory

    return get_mediator()


@given('a clean database')
def clean_database(context):
    """Initialize clean in-memory database with SQLAlchemy"""
    from adapters.secondary.persistence.models import metadata

    reset_container()

    # Initialize SQLAlchemy schema
    engine = get_engine()
    metadata.create_all(engine)

    context['api_calls'] = []


@given('a test player exists')
def create_test_player(context):
    """Create a test player using SQLAlchemy repository"""
    player_repo = get_player_repository()
    player = Player(
        player_id=None,
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=datetime.now(timezone.utc),
        last_active=None,
        metadata={}
    )
    created_player = player_repo.create(player)
    context['player_id'] = created_player.player_id


@given(parsers.parse('an unaccepted contract "{contract_id}" in the database'))
def unaccepted_contract(context, contract_id):
    """Create an unaccepted contract using SQLAlchemy repository"""
    contract_repo = get_contract_repository()

    delivery = Delivery(
        trade_symbol="IRON_ORE",
        destination=Waypoint("X1-TEST-DEST", 10.0, 20.0),
        units_required=100,
        units_fulfilled=0
    )

    payment = Payment(on_accepted=10000, on_fulfilled=50000)

    terms = ContractTerms(
        deadline=datetime.now(timezone.utc) + timedelta(days=7),
        payment=payment,
        deliveries=[delivery]
    )

    contract = Contract(
        contract_id=contract_id,
        faction_symbol='COSMIC',
        type='PROCUREMENT',
        terms=terms,
        accepted=False,
        fulfilled=False,
        deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
    )

    contract_repo.save(contract, context['player_id'])
    context['contract_id'] = contract_id


@given('the API will successfully accept the contract')
def mock_accept_api(context):
    """Mock API to accept contract"""
    mock_api = Mock()
    mock_api.accept_contract = Mock(return_value={
        'data': {
            'contract': {
                'id': context.get('contract_id', 'contract-123'),
                'accepted': True
            }
        }
    })
    context['mock_api'] = mock_api


@given(parsers.parse('an accepted contract "{contract_id}" with delivery requirements'))
def accepted_contract_with_delivery(context, contract_id):
    """Create an accepted contract with delivery requirements"""
    contract_repo = get_contract_repository()

    delivery = Delivery(
        trade_symbol="IRON_ORE",
        destination=Waypoint("X1-TEST-DEST", 10.0, 20.0),
        units_required=100,
        units_fulfilled=0
    )

    payment = Payment(on_accepted=10000, on_fulfilled=50000)

    terms = ContractTerms(
        deadline=datetime.now(timezone.utc) + timedelta(days=7),
        payment=payment,
        deliveries=[delivery]
    )

    contract = Contract(
        contract_id=contract_id,
        faction_symbol='COSMIC',
        type='PROCUREMENT',
        terms=terms,
        accepted=False,
        fulfilled=False,
        deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
    )

    contract.accept()
    contract_repo.save(contract, context['player_id'])
    context['contract_id'] = contract_id


@given('the API will successfully record the delivery')
def mock_deliver_api(context):
    """Mock API to record delivery"""
    mock_api = Mock()
    mock_api.deliver_contract = Mock(return_value={
        'data': {
            'contract': {
                'terms': {
                    'deliver': [{
                        'tradeSymbol': 'IRON_ORE',
                        'unitsFulfilled': 50,
                        'unitsRequired': 100
                    }]
                }
            }
        }
    })
    context['mock_api'] = mock_api


@given(parsers.parse('a fully delivered contract "{contract_id}"'))
def fully_delivered_contract(context, contract_id):
    """Create a contract with all deliveries fulfilled"""
    contract_repo = get_contract_repository()

    delivery = Delivery(
        trade_symbol="IRON_ORE",
        destination=Waypoint("X1-TEST-DEST", 10.0, 20.0),
        units_required=100,
        units_fulfilled=100  # Fully delivered
    )

    payment = Payment(on_accepted=10000, on_fulfilled=50000)

    terms = ContractTerms(
        deadline=datetime.now(timezone.utc) + timedelta(days=7),
        payment=payment,
        deliveries=[delivery]
    )

    contract = Contract(
        contract_id=contract_id,
        faction_symbol='COSMIC',
        type='PROCUREMENT',
        terms=terms,
        accepted=True,
        fulfilled=False,  # Not yet fulfilled
        deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
    )

    contract_repo.save(contract, context['player_id'])
    context['contract_id'] = contract_id


@given('the API will successfully fulfill the contract')
def mock_fulfill_api(context):
    """Mock API to fulfill contract"""
    mock_api = Mock()
    mock_api.fulfill_contract = Mock(return_value={
        'data': {
            'contract': {
                'id': context.get('contract_id', 'contract-123'),
                'fulfilled': True
            }
        }
    })
    context['mock_api'] = mock_api


@given(parsers.parse('a ship "{ship_symbol}" at a location'))
def ship_at_location(context, ship_symbol):
    """Create a ship at a location"""
    context['ship_symbol'] = ship_symbol


@given('the API will successfully negotiate a contract')
def mock_negotiate_api(context):
    """Mock API to negotiate contract"""
    mock_api = Mock()
    mock_api.negotiate_contract = Mock(return_value={
        'data': {
            'contract': {
                'id': 'new-contract-123',
                'factionSymbol': 'COSMIC',
                'type': 'PROCUREMENT',
                'accepted': False,
                'fulfilled': False,
                'deadlineToAccept': (datetime.now(timezone.utc) + timedelta(days=1)).isoformat(),
                'terms': {
                    'deadline': (datetime.now(timezone.utc) + timedelta(days=7)).isoformat(),
                    'payment': {
                        'onAccepted': 10000,
                        'onFulfilled': 50000
                    },
                    'deliver': [{
                        'tradeSymbol': 'IRON_ORE',
                        'destinationSymbol': 'X1-TEST-DEST',
                        'unitsRequired': 100,
                        'unitsFulfilled': 0
                    }]
                }
            }
        }
    })
    context['mock_api'] = mock_api


@when(parsers.parse('I execute AcceptContractCommand for "{contract_id}"'))
def execute_accept_command(context, contract_id, mediator):
    """Execute AcceptContractCommand"""
    command = AcceptContractCommand(
        contract_id=contract_id,
        player_id=context['player_id']
    )
    try:
        context['result'] = asyncio.run(mediator.send_async(command))
        context['command_success'] = True
    except Exception as e:
        context['command_success'] = False
        context['error'] = e


@when(parsers.parse('I execute DeliverContractCommand for "{contract_id}" with {units:d} units of "{trade_symbol}" from ship "{ship_symbol}"'))
def execute_deliver_command(context, contract_id, units, trade_symbol, ship_symbol, mediator):
    """Execute DeliverContractCommand"""
    command = DeliverContractCommand(
        contract_id=contract_id,
        ship_symbol=ship_symbol,
        trade_symbol=trade_symbol,
        units=units,
        player_id=context['player_id']
    )
    try:
        context['result'] = asyncio.run(mediator.send_async(command))
        context['command_success'] = True
    except Exception as e:
        context['command_success'] = False
        context['error'] = e


@when(parsers.parse('I execute FulfillContractCommand for "{contract_id}"'))
def execute_fulfill_command(context, contract_id, mediator):
    """Execute FulfillContractCommand"""
    command = FulfillContractCommand(
        contract_id=contract_id,
        player_id=context['player_id']
    )
    try:
        context['result'] = asyncio.run(mediator.send_async(command))
        context['command_success'] = True
    except Exception as e:
        context['command_success'] = False
        context['error'] = e


@when(parsers.parse('I execute NegotiateContractCommand for ship "{ship_symbol}"'))
def execute_negotiate_command(context, ship_symbol, mediator):
    """Execute NegotiateContractCommand"""
    command = NegotiateContractCommand(
        ship_symbol=ship_symbol,
        player_id=context['player_id']
    )
    try:
        context['result'] = asyncio.run(mediator.send_async(command))
        context['command_success'] = True
    except Exception as e:
        context['command_success'] = False
        context['error'] = e


@then('the command should succeed')
def check_command_success(context):
    """Verify command succeeded"""
    assert context.get('command_success', False) is True, \
        f"Command failed with error: {context.get('error', 'Unknown')}"


@then('the contract should be marked as accepted in the database')
def check_contract_accepted(context):
    """Verify contract is accepted in database"""
    contract_repo = get_contract_repository()
    contract = contract_repo.find_by_id(context['contract_id'], context['player_id'])
    assert contract is not None
    assert contract.accepted is True




@then('the contract delivery progress should be updated in the database')
def check_delivery_progress(context):
    """Verify delivery progress updated"""
    contract_repo = get_contract_repository()
    contract = contract_repo.find_by_id(context['contract_id'], context['player_id'])
    assert contract is not None
    # Check that at least one delivery has progress
    assert any(d.units_fulfilled > 0 for d in contract.terms.deliveries)




@then('the contract should be marked as fulfilled in the database')
def check_contract_fulfilled(context):
    """Verify contract is fulfilled in database"""
    contract_repo = get_contract_repository()
    contract = contract_repo.find_by_id(context['contract_id'], context['player_id'])
    assert contract is not None
    assert contract.fulfilled is True




@then('a new contract should be saved in the database')
def check_new_contract_saved(context):
    """Verify new contract was saved"""
    contract_repo = get_contract_repository()
    contracts = contract_repo.find_all(context['player_id'])
    # Should have at least one contract (the negotiated one)
    assert len(contracts) > 0


