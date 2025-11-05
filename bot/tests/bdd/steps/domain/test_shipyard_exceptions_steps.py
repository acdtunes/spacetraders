"""Step definitions for shipyard domain exceptions feature."""

import pytest
from pytest_bdd import scenarios, when, then, parsers

from domain.shared.exceptions import DomainException

# Try to import the exceptions - these will fail initially (RED phase)
try:
    from domain.shared.exceptions import (
        InsufficientCreditsError,
        ShipNotAvailableError,
        ShipyardNotFoundError
    )
except ImportError:
    # Expected to fail initially - TDD RED phase
    InsufficientCreditsError = None
    ShipNotAvailableError = None
    ShipyardNotFoundError = None


# Load all scenarios from the feature file
scenarios('../../features/domain/shipyard_exceptions.feature')


# ============================================================================
# Context Fixture
# ============================================================================

@pytest.fixture
def context():
    """Shared context for test state"""
    return {
        'exception': None,
        'exception_message': None,
        'inheritance_check': None
    }


# ============================================================================
# InsufficientCreditsError Steps
# ============================================================================

@when("I check if InsufficientCreditsError inherits from DomainException")
def check_insufficient_credits_inheritance(context):
    """Check if InsufficientCreditsError inherits from DomainException"""
    if InsufficientCreditsError is None:
        pytest.fail("InsufficientCreditsError not yet implemented")
    context['inheritance_check'] = issubclass(InsufficientCreditsError, DomainException)


@when(parsers.parse('I raise InsufficientCreditsError with message "{message}"'))
def raise_insufficient_credits(context, message):
    """Raise InsufficientCreditsError with message"""
    if InsufficientCreditsError is None:
        pytest.fail("InsufficientCreditsError not yet implemented")
    try:
        raise InsufficientCreditsError(message)
    except InsufficientCreditsError as e:
        context['exception'] = e
        context['exception_message'] = str(e)


# ============================================================================
# ShipNotAvailableError Steps
# ============================================================================

@when("I check if ShipNotAvailableError inherits from DomainException")
def check_ship_not_available_inheritance(context):
    """Check if ShipNotAvailableError inherits from DomainException"""
    if ShipNotAvailableError is None:
        pytest.fail("ShipNotAvailableError not yet implemented")
    context['inheritance_check'] = issubclass(ShipNotAvailableError, DomainException)


@when(parsers.parse('I raise ShipNotAvailableError with message "{message}"'))
def raise_ship_not_available(context, message):
    """Raise ShipNotAvailableError with message"""
    if ShipNotAvailableError is None:
        pytest.fail("ShipNotAvailableError not yet implemented")
    try:
        raise ShipNotAvailableError(message)
    except ShipNotAvailableError as e:
        context['exception'] = e
        context['exception_message'] = str(e)


# ============================================================================
# ShipyardNotFoundError Steps
# ============================================================================

@when("I check if ShipyardNotFoundError inherits from DomainException")
def check_shipyard_not_found_inheritance(context):
    """Check if ShipyardNotFoundError inherits from DomainException"""
    if ShipyardNotFoundError is None:
        pytest.fail("ShipyardNotFoundError not yet implemented")
    context['inheritance_check'] = issubclass(ShipyardNotFoundError, DomainException)


@when(parsers.parse('I raise ShipyardNotFoundError with message "{message}"'))
def raise_shipyard_not_found(context, message):
    """Raise ShipyardNotFoundError with message"""
    if ShipyardNotFoundError is None:
        pytest.fail("ShipyardNotFoundError not yet implemented")
    try:
        raise ShipyardNotFoundError(message)
    except ShipyardNotFoundError as e:
        context['exception'] = e
        context['exception_message'] = str(e)


# ============================================================================
# Common Assertion Steps
# ============================================================================

@then("it should be True")
def assert_true(context):
    """Assert the result is True"""
    assert context['inheritance_check'] is True


@then(parsers.parse('the exception message should be "{expected_message}"'))
def assert_exception_message(context, expected_message):
    """Assert the exception message matches"""
    assert context['exception_message'] == expected_message


@then("it should be catchable as DomainException")
def assert_catchable_as_domain_exception(context):
    """Assert the exception is catchable as DomainException"""
    assert isinstance(context['exception'], DomainException)
