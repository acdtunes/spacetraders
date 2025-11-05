"""BDD step definitions for Contract Repository"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from datetime import datetime, timezone, timedelta

from spacetraders.domain.shared.contract import (
    Contract,
    ContractTerms,
    Delivery,
    Payment
)
from spacetraders.domain.shared.value_objects import Waypoint
from spacetraders.adapters.secondary.persistence.database import Database
from spacetraders.adapters.secondary.persistence.contract_repository import ContractRepository
from spacetraders.adapters.secondary.persistence.player_repository import PlayerRepository
from spacetraders.domain.shared.player import Player

# Load scenarios
scenarios('../../../features/integration/persistence/contract_repository.feature')


@pytest.fixture
def contract_repo(context):
    """Create contract repository"""
    db = context['db']
    return ContractRepository(db)


@given('a clean database')
def clean_database(context):
    """Initialize clean in-memory database"""
    context['db'] = Database(":memory:")


@given('a test player exists')
def create_test_player(context):
    """Create a test player"""
    db = context['db']
    player_repo = PlayerRepository(db)
    player = Player(
        player_id=None,  # Will be assigned by create
        agent_symbol="TEST_AGENT",
        token="test-token",
        created_at=datetime.now(timezone.utc),
        last_active=None,
        metadata={}
    )
    created_player = player_repo.create(player)
    context['player_id'] = created_player.player_id


@given('a contract entity with valid data')
def contract_with_valid_data(context):
    """Create a contract entity"""
    delivery = Delivery(
        trade_symbol="IRON_ORE",
        destination=Waypoint("X1-TEST-DEST", 10.0, 20.0),
        units_required=100,
        units_fulfilled=0
    )

    payment = Payment(
        on_accepted=10000,
        on_fulfilled=50000
    )

    terms = ContractTerms(
        deadline=datetime.now(timezone.utc) + timedelta(days=7),
        payment=payment,
        deliveries=[delivery]
    )

    contract = Contract(
        contract_id='contract-123',
        faction_symbol='COSMIC',
        type='PROCUREMENT',
        terms=terms,
        accepted=False,
        fulfilled=False,
        deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
    )

    context['contract'] = contract
    context['contract_id'] = 'contract-123'


@when('I save the contract to the repository')
def save_contract(context, contract_repo):
    """Save contract to repository"""
    contract_repo.save(context['contract'], context['player_id'])


@when('I retrieve the contract by ID')
def retrieve_contract(context, contract_repo):
    """Retrieve contract by ID"""
    context['retrieved_contract'] = contract_repo.find_by_id(
        context['contract_id'],
        context['player_id']
    )


@then('the retrieved contract should match the saved contract')
def check_retrieved_contract(context):
    """Verify retrieved contract matches"""
    original = context['contract']
    retrieved = context['retrieved_contract']

    assert retrieved is not None
    assert retrieved.contract_id == original.contract_id
    assert retrieved.faction_symbol == original.faction_symbol
    assert retrieved.type == original.type
    assert retrieved.accepted == original.accepted
    assert retrieved.fulfilled == original.fulfilled


@then('all contract properties should be preserved')
def check_all_properties(context):
    """Verify all properties are preserved"""
    original = context['contract']
    retrieved = context['retrieved_contract']

    # Check terms
    assert retrieved.terms.payment.on_accepted == original.terms.payment.on_accepted
    assert retrieved.terms.payment.on_fulfilled == original.terms.payment.on_fulfilled

    # Check deliveries
    assert len(retrieved.terms.deliveries) == len(original.terms.deliveries)
    for i, delivery in enumerate(retrieved.terms.deliveries):
        orig_delivery = original.terms.deliveries[i]
        assert delivery.trade_symbol == orig_delivery.trade_symbol
        assert delivery.units_required == orig_delivery.units_required
        assert delivery.units_fulfilled == orig_delivery.units_fulfilled


@when(parsers.parse('I try to find contract "{contract_id}"'))
def try_find_contract(context, contract_id, contract_repo):
    """Try to find contract by ID"""
    context['result'] = contract_repo.find_by_id(contract_id, context['player_id'])


@then('the result should be None')
def result_is_none(context):
    """Verify result is None"""
    assert context['result'] is None


@given(parsers.parse('{count:d} contracts exist for the player'))
def create_contracts(context, count, contract_repo):
    """Create multiple contracts"""
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


@when('I list all contracts for the player')
def list_contracts(context, contract_repo):
    """List all contracts"""
    context['contracts'] = contract_repo.find_all(context['player_id'])


@then(parsers.parse('I should get {count:d} contracts'))
def check_contract_count(context, count):
    """Verify contract count"""
    assert len(context['contracts']) == count


@given('a saved contract')
def saved_contract(context, contract_repo):
    """Create and save a contract"""
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
        contract_id='contract-update-test',
        faction_symbol='COSMIC',
        type='PROCUREMENT',
        terms=terms,
        accepted=False,
        fulfilled=False,
        deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
    )

    contract_repo.save(contract, context['player_id'])
    context['contract'] = contract
    context['contract_id'] = 'contract-update-test'


@when('I update the contract delivery status')
def update_delivery_status(context):
    """Update contract with new delivery status"""
    # Create updated delivery
    updated_delivery = Delivery(
        trade_symbol="IRON_ORE",
        destination=Waypoint("X1-TEST-DEST", 10.0, 20.0),
        units_required=100,
        units_fulfilled=50  # Updated
    )

    payment = Payment(on_accepted=10000, on_fulfilled=50000)

    terms = ContractTerms(
        deadline=context['contract'].terms.deadline,
        payment=payment,
        deliveries=[updated_delivery]
    )

    # Create new contract with updated data (immutable)
    updated_contract = Contract(
        contract_id=context['contract'].contract_id,
        faction_symbol=context['contract'].faction_symbol,
        type=context['contract'].type,
        terms=terms,
        accepted=True,  # Also accept it
        fulfilled=False,
        deadline_to_accept=context['contract'].deadline_to_accept
    )

    context['contract'] = updated_contract


@when('I save the updated contract')
def save_updated_contract(context, contract_repo):
    """Save the updated contract"""
    contract_repo.save(context['contract'], context['player_id'])


@then('the retrieved contract should reflect the updates')
def check_updates(context):
    """Verify updates were saved"""
    retrieved = context['retrieved_contract']

    assert retrieved.accepted is True
    assert retrieved.terms.deliveries[0].units_fulfilled == 50


@given(parsers.parse('a contract with {count:d} delivery requirements'))
def contract_with_multiple_deliveries(context, count):
    """Create contract with multiple deliveries"""
    deliveries = []
    for i in range(count):
        delivery = Delivery(
            trade_symbol=f"GOOD_{i}",
            destination=Waypoint(f"X1-DEST-{i}", 10.0 + i, 20.0 + i),
            units_required=100 * (i + 1),
            units_fulfilled=0
        )
        deliveries.append(delivery)

    payment = Payment(on_accepted=10000, on_fulfilled=50000)

    terms = ContractTerms(
        deadline=datetime.now(timezone.utc) + timedelta(days=7),
        payment=payment,
        deliveries=deliveries
    )

    contract = Contract(
        contract_id='multi-delivery-contract',
        faction_symbol='COSMIC',
        type='PROCUREMENT',
        terms=terms,
        accepted=False,
        fulfilled=False,
        deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
    )

    context['contract'] = contract
    context['contract_id'] = 'multi-delivery-contract'


@then(parsers.parse('the contract should have {count:d} deliveries'))
def check_delivery_count(context, count):
    """Verify delivery count"""
    retrieved = context['retrieved_contract']
    assert len(retrieved.terms.deliveries) == count


@then('all delivery details should be preserved')
def check_delivery_details(context):
    """Verify all delivery details"""
    original = context['contract']
    retrieved = context['retrieved_contract']

    for i, delivery in enumerate(retrieved.terms.deliveries):
        orig = original.terms.deliveries[i]
        assert delivery.trade_symbol == orig.trade_symbol
        assert delivery.destination.symbol == orig.destination.symbol
        assert delivery.units_required == orig.units_required
        assert delivery.units_fulfilled == orig.units_fulfilled
