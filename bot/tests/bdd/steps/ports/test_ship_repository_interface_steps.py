"""Step definitions for ship repository interface verification"""
import inspect
from pytest_bdd import scenarios, given, then, parsers

from ports.outbound.repositories import IShipRepository

scenarios('../../features/ports/ship_repository_interface.feature')


@given('the IShipRepository interface')
def ship_repository_interface(context):
    """Store the IShipRepository interface for inspection"""
    context.interface = IShipRepository
    return context


@then(parsers.parse('it should have method "{method_name}"'))
def should_have_method(context, method_name):
    """Verify interface has the specified method"""
    # Check if method exists in the abstract base class
    assert hasattr(context.interface, method_name), \
        f"IShipRepository should have method '{method_name}'"

    # Verify it's an abstract method
    method = getattr(context.interface, method_name)
    assert hasattr(method, '__isabstractmethod__'), \
        f"Method '{method_name}' should be abstract"


@then(parsers.parse('it should not have method "{method_name}"'))
def should_not_have_method(context, method_name):
    """Verify interface does not have the specified method"""
    # Check if method exists in the abstract base class
    if hasattr(context.interface, method_name):
        # If it exists, it might be inherited from ABC or object, which is ok
        # But if it's an abstract method defined on this class, that's a failure
        method = getattr(context.interface, method_name)
        assert not hasattr(method, '__isabstractmethod__') or \
               method_name in ['__init__', '__new__', '__del__'], \
            f"IShipRepository should not have method '{method_name}'"
    # If method doesn't exist at all, that's what we want
