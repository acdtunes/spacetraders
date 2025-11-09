"""BDD step definitions for Contract Queries"""
import asyncio
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from datetime import datetime, timezone, timedelta

from domain.shared.contract import (
    Contract,
    ContractTerms,
    Delivery,
    Payment
)
from domain.shared.value_objects import Waypoint
from domain.shared.player import Player
from application.contracts.queries.get_contract import GetContractQuery
from application.contracts.queries.list_contracts import ListContractsQuery
from application.contracts.queries.get_active_contracts import GetActiveContractsQuery
from configuration.container import (
    get_mediator,
    get_player_repository,
    get_contract_repository,
    reset_container,
    get_engine
)

# Load scenarios
scenarios('../../features/application/contract_queries.feature')


@pytest.fixture
def mediator(context):
    """Get mediator instance with SQLAlchemy"""
    return get_mediator()


@given('a clean database')
def clean_database(context):
    """Initialize clean in-memory database with SQLAlchemy"""
    from adapters.secondary.persistence.models import metadata

    reset_container()

    # Initialize SQLAlchemy schema
    engine = get_engine()
    metadata.create_all(engine)


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


@given(parsers.parse('a saved contract with ID "{contract_id}"'))
def saved_contract_with_id(context, contract_id):
    """Create and save a contract with specific ID"""
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


@given(parsers.parse('{count:d} saved contracts for the player'))
def saved_contracts(context, count):
    """Create and save multiple contracts"""
    contract_repo = get_contract_repository()

    for i in range(count):
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
            contract_id=f'contract-{i}',
            faction_symbol='COSMIC',
            type='PROCUREMENT',
            terms=terms,
            accepted=False,
            fulfilled=False,
            deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
        )

        contract_repo.save(contract, context['player_id'])


@given(parsers.parse('{count:d} accepted contracts for the player'))
def accepted_contracts(context, count):
    """Create and save accepted contracts"""
    contract_repo = get_contract_repository()

    for i in range(count):
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
            contract_id=f'accepted-{i}',
            faction_symbol='COSMIC',
            type='PROCUREMENT',
            terms=terms,
            accepted=False,
            fulfilled=False,
            deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
        )

        # Accept the contract
        contract.accept()

        contract_repo.save(contract, context['player_id'])


@given(parsers.parse('{count:d} fulfilled contract for the player'))
def fulfilled_contract(context, count):
    """Create and save a fulfilled contract"""
    contract_repo = get_contract_repository()

    for i in range(count):
        delivery = Delivery(
            trade_symbol="IRON_ORE",
            destination=Waypoint("X1-TEST-DEST", 10.0, 20.0),
            units_required=100,
            units_fulfilled=100  # Fully fulfilled
        )

        payment = Payment(on_accepted=10000, on_fulfilled=50000)

        terms = ContractTerms(
            deadline=datetime.now(timezone.utc) + timedelta(days=7),
            payment=payment,
            deliveries=[delivery]
        )

        contract = Contract(
            contract_id=f'fulfilled-{i}',
            faction_symbol='COSMIC',
            type='PROCUREMENT',
            terms=terms,
            accepted=True,
            fulfilled=True,
            deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
        )

        contract_repo.save(contract, context['player_id'])


@given(parsers.parse('{count:d} unaccepted contract for the player'))
def unaccepted_contract(context, count):
    """Create and save an unaccepted contract"""
    contract_repo = get_contract_repository()

    for i in range(count):
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
            contract_id=f'unaccepted-{i}',
            faction_symbol='COSMIC',
            type='PROCUREMENT',
            terms=terms,
            accepted=False,
            fulfilled=False,
            deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
        )

        contract_repo.save(contract, context['player_id'])


@when(parsers.parse('I query for contract "{contract_id}"'))
def query_contract(context, contract_id, mediator):
    """Query for contract by ID"""
    query = GetContractQuery(
        contract_id=contract_id,
        player_id=context['player_id']
    )
    context['result'] = asyncio.run(mediator.send_async(query))


@when('I query for all contracts')
def query_all_contracts(context, mediator):
    """Query for all contracts"""
    query = ListContractsQuery(player_id=context['player_id'])
    context['result'] = asyncio.run(mediator.send_async(query))


@when('I query for active contracts')
def query_active_contracts(context, mediator):
    """Query for active contracts"""
    query = GetActiveContractsQuery(player_id=context['player_id'])
    context['result'] = asyncio.run(mediator.send_async(query))


@then('I should receive the contract details')
def check_contract_details(context):
    """Verify contract details received"""
    assert context['result'] is not None
    assert isinstance(context['result'], Contract)


@then(parsers.parse('the contract should have ID "{contract_id}"'))
def check_contract_id(context, contract_id):
    """Verify contract ID"""
    assert context['result'].contract_id == contract_id


@then('I should receive None')
def check_none_result(context):
    """Verify None result"""
    assert context['result'] is None


@then(parsers.parse('I should receive {count:d} contracts'))
def check_contract_count(context, count):
    """Verify contract count"""
    assert len(context['result']) == count


@then('all returned contracts should be accepted')
def check_all_accepted(context):
    """Verify all contracts are accepted"""
    for contract in context['result']:
        assert contract.accepted is True


@then('all returned contracts should not be fulfilled')
def check_not_fulfilled(context):
    """Verify contracts are not fulfilled"""
    for contract in context['result']:
        assert contract.fulfilled is False
