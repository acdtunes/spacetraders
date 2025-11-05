"""BDD step definitions for Contract entity"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from datetime import datetime, timezone, timedelta

from domain.shared.contract import (
    Contract,
    ContractTerms,
    Delivery,
    Payment,
    ContractStatus,
    ContractAlreadyAcceptedError
)
from domain.shared.value_objects import Waypoint

# Load scenarios
scenarios('../../features/domain/contract_entity.feature')


@given('a contract with valid terms')
def contract_with_valid_terms(context):
    """Create contract data with valid terms"""
    delivery = Delivery(
        trade_symbol="IRON_ORE",
        destination=Waypoint("X1-TEST-DEST", 10, 20),
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

    context['contract_data'] = {
        'contract_id': 'contract-123',
        'faction_symbol': 'COSMIC',
        'type': 'PROCUREMENT',
        'terms': terms,
        'accepted': False,
        'fulfilled': False,
        'deadline_to_accept': datetime.now(timezone.utc) + timedelta(days=1)
    }


@when('I create the contract entity')
def create_contract_entity(context):
    """Create Contract entity from data"""
    context['contract'] = Contract(**context['contract_data'])


@then('the contract should be created successfully')
def contract_created_successfully(context):
    """Verify contract was created"""
    assert context['contract'] is not None
    assert context['contract'].contract_id == 'contract-123'


@then(parsers.parse('the contract status should be "{status}"'))
def check_contract_status(context, status):
    """Verify contract status"""
    expected_status = ContractStatus[status]
    assert context['contract'].status == expected_status


@given(parsers.parse('a contract with status "{status}"'))
def contract_with_status(context, status):
    """Create contract with specific status"""
    delivery = Delivery(
        trade_symbol="IRON_ORE",
        destination=Waypoint("X1-TEST-DEST", 10, 20),
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

    is_accepted = (status == "ACCEPTED")

    context['contract'] = Contract(
        contract_id='contract-123',
        faction_symbol='COSMIC',
        type='PROCUREMENT',
        terms=terms,
        accepted=is_accepted,
        fulfilled=False,
        deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
    )


@when('I accept the contract')
def accept_contract(context):
    """Accept the contract"""
    context['contract'].accept()


@then('the contract should be marked as accepted')
def contract_marked_accepted(context):
    """Verify contract is marked as accepted"""
    assert context['contract'].accepted is True


@when('I try to accept the contract')
def try_accept_contract(context):
    """Try to accept contract and capture exception"""
    try:
        context['contract'].accept()
        context['exception'] = None
    except Exception as e:
        context['exception'] = e


@then('it should raise ContractAlreadyAcceptedError')
def should_raise_already_accepted_error(context):
    """Verify ContractAlreadyAcceptedError was raised"""
    assert context['exception'] is not None
    assert isinstance(context['exception'], ContractAlreadyAcceptedError)


@given('a contract with delivery requirements')
def contract_with_delivery_requirements(context):
    """Create contract with delivery requirements"""
    delivery = Delivery(
        trade_symbol="IRON_ORE",
        destination=Waypoint("X1-TEST-DEST", 10, 20),
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

    context['contract'] = Contract(
        contract_id='contract-123',
        faction_symbol='COSMIC',
        type='PROCUREMENT',
        terms=terms,
        accepted=True,
        fulfilled=False,
        deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
    )


@given('all delivery units are fulfilled')
def all_delivery_units_fulfilled(context):
    """Mark all deliveries as fulfilled"""
    # Update the delivery to have all units fulfilled
    delivery = Delivery(
        trade_symbol="IRON_ORE",
        destination=Waypoint("X1-TEST-DEST", 10, 20),
        units_required=100,
        units_fulfilled=100  # All fulfilled
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

    context['contract'] = Contract(
        contract_id='contract-123',
        faction_symbol='COSMIC',
        type='PROCUREMENT',
        terms=terms,
        accepted=True,
        fulfilled=False,
        deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
    )


@when('I check if the contract is fulfilled')
def check_if_contract_fulfilled(context):
    """Check if contract is fulfilled"""
    context['is_fulfilled'] = context['contract'].is_fulfilled()


@then('it should return True')
def should_return_true(context):
    """Verify result is True"""
    assert context['is_fulfilled'] is True


@given(parsers.parse('a contract requiring {units:d} units of {trade_symbol}'))
def contract_requiring_units(context, units, trade_symbol):
    """Create contract requiring specific units"""
    delivery = Delivery(
        trade_symbol=trade_symbol,
        destination=Waypoint("X1-TEST-DEST", 10, 20),
        units_required=units,
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

    context['contract'] = Contract(
        contract_id='contract-123',
        faction_symbol='COSMIC',
        type='PROCUREMENT',
        terms=terms,
        accepted=True,
        fulfilled=False,
        deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
    )


@given(parsers.parse('{units:d} units have been fulfilled'))
def units_have_been_fulfilled(context, units):
    """Update contract with fulfilled units"""
    # Recreate contract with updated fulfilled units
    delivery = Delivery(
        trade_symbol=context['contract'].terms.deliveries[0].trade_symbol,
        destination=Waypoint("X1-TEST-DEST", 10, 20),
        units_required=context['contract'].terms.deliveries[0].units_required,
        units_fulfilled=units
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

    context['contract'] = Contract(
        contract_id='contract-123',
        faction_symbol='COSMIC',
        type='PROCUREMENT',
        terms=terms,
        accepted=True,
        fulfilled=False,
        deadline_to_accept=datetime.now(timezone.utc) + timedelta(days=1)
    )


@when('I check remaining units')
def check_remaining_units(context):
    """Check remaining units needed"""
    context['remaining_units'] = context['contract'].remaining_units()


@then(parsers.parse('it should return {units:d} units'))
def should_return_units(context, units):
    """Verify remaining units"""
    assert context['remaining_units'] == units
