"""Step definitions for jettison API interface tests"""
import pytest
import inspect
from pytest_bdd import scenarios, given, then, parsers

from ports.outbound.api_client import ISpaceTradersAPI
from adapters.secondary.api.client import SpaceTradersAPIClient

# Load scenarios
scenarios('../../../features/application/cargo/jettison_api_interface.feature')


@pytest.fixture
def context():
    """Provide shared context dictionary for test data"""
    return {}


@given('the ISpaceTradersAPI interface exists')
def interface_exists(context):
    """Verify interface exists"""
    context['interface'] = ISpaceTradersAPI


@given('the SpaceTradersAPIClient exists')
def client_exists(context):
    """Verify client exists"""
    context['client_class'] = SpaceTradersAPIClient


@then('the interface should have a jettison_cargo method')
def interface_has_jettison_method(context):
    """Verify interface declares jettison_cargo method"""
    assert hasattr(context['interface'], 'jettison_cargo'), \
        "ISpaceTradersAPI interface must declare jettison_cargo method"


@then('the method should accept ship_symbol as a parameter')
def method_accepts_ship_symbol(context):
    """Verify method signature includes ship_symbol"""
    method = getattr(context['interface'], 'jettison_cargo')
    sig = inspect.signature(method)
    params = list(sig.parameters.keys())
    assert 'ship_symbol' in params, \
        f"jettison_cargo must accept ship_symbol parameter. Got: {params}"


@then('the method should accept cargo_symbol as a parameter')
def method_accepts_cargo_symbol(context):
    """Verify method signature includes cargo_symbol"""
    method = getattr(context['interface'], 'jettison_cargo')
    sig = inspect.signature(method)
    params = list(sig.parameters.keys())
    assert 'cargo_symbol' in params, \
        f"jettison_cargo must accept cargo_symbol parameter. Got: {params}"


@then('the method should accept units as a parameter')
def method_accepts_units(context):
    """Verify method signature includes units"""
    method = getattr(context['interface'], 'jettison_cargo')
    sig = inspect.signature(method)
    params = list(sig.parameters.keys())
    assert 'units' in params, \
        f"jettison_cargo must accept units parameter. Got: {params}"


@then('the client should implement jettison_cargo method')
def client_implements_jettison(context):
    """Verify client implements jettison_cargo method"""
    assert hasattr(context['client_class'], 'jettison_cargo'), \
        "SpaceTradersAPIClient must implement jettison_cargo method"


@then('the method should call POST endpoint for jettison')
def method_calls_post_endpoint(context):
    """Verify method implementation makes POST request"""
    # Get the method source code
    method = getattr(context['client_class'], 'jettison_cargo')
    source = inspect.getsource(method)

    # Verify it uses POST method
    assert 'POST' in source, "jettison_cargo should use POST method"
    assert '/jettison' in source, "jettison_cargo should call /jettison endpoint"
