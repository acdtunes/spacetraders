"""Step definitions for shipyard value objects feature."""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from dataclasses import FrozenInstanceError

from domain.shared.shipyard import ShipListing, Shipyard

# Load scenarios
scenarios('../../../features/domain/shipyard/shipyard_value_objects.feature')


# Fixtures
@pytest.fixture
def context():
    """Shared context for steps."""
    return {}


# ShipListing steps
@given(parsers.parse('a ship listing with type "{ship_type}"'))
def ship_listing_with_type(context, ship_type):
    """Store ship listing type."""
    context['ship_type'] = ship_type


@given(parsers.parse('the listing has name "{name}"'))
def listing_has_name(context, name):
    """Store listing name."""
    context['listing_name'] = name


@given(parsers.parse('the listing has description "{description}"'))
def listing_has_description(context, description):
    """Store listing description."""
    context['listing_description'] = description


@given(parsers.parse('the listing has purchase price {price:d}'))
def listing_has_price(context, price):
    """Store listing price."""
    context['listing_price'] = price


@when('I create the ship listing')
def create_ship_listing(context):
    """Create the ship listing."""
    context['ship_listing'] = ShipListing(
        ship_type=context['ship_type'],
        name=context['listing_name'],
        description=context['listing_description'],
        purchase_price=context['listing_price']
    )


@then(parsers.parse('the ship listing should have type "{expected_type}"'))
def verify_listing_type(context, expected_type):
    """Verify the listing type."""
    assert context['ship_listing'].ship_type == expected_type


@then(parsers.parse('the ship listing should have name "{expected_name}"'))
def verify_listing_name(context, expected_name):
    """Verify the listing name."""
    assert context['ship_listing'].name == expected_name


@then(parsers.parse('the ship listing should have description "{expected_description}"'))
def verify_listing_description(context, expected_description):
    """Verify the listing description."""
    assert context['ship_listing'].description == expected_description


@then(parsers.parse('the ship listing should have purchase price {expected_price:d}'))
def verify_listing_price(context, expected_price):
    """Verify the listing price."""
    assert context['ship_listing'].purchase_price == expected_price


# ShipListing immutability
@given(parsers.parse('a created ship listing with type "{ship_type}"'))
def created_ship_listing(context, ship_type):
    """Create a ship listing."""
    context['ship_listing'] = ShipListing(
        ship_type=ship_type,
        name="Test Ship",
        description="Test Description",
        purchase_price=10000
    )


@when('I attempt to modify the ship listing type')
def attempt_modify_listing(context):
    """Attempt to modify the ship listing."""
    try:
        context['ship_listing'].ship_type = "SHIP_DIFFERENT_TYPE"
        context['modification_error'] = None
    except (FrozenInstanceError, AttributeError) as e:
        context['modification_error'] = e


@then('the modification should be rejected')
def verify_modification_rejected(context):
    """Verify the modification was rejected."""
    assert context['modification_error'] is not None


# Shipyard steps
@given(parsers.parse('a shipyard at waypoint "{waypoint_symbol}"'))
def shipyard_at_waypoint(context, waypoint_symbol):
    """Store shipyard waypoint."""
    context['shipyard_symbol'] = waypoint_symbol


@given(parsers.parse('the shipyard has ship types {ship_types}'))
def shipyard_has_ship_types(context, ship_types):
    """Store shipyard ship types."""
    # Parse the list from string representation
    import ast
    context['shipyard_ship_types'] = ast.literal_eval(ship_types)


@given(parsers.parse('the shipyard has modification fee {fee:d}'))
def shipyard_has_modification_fee(context, fee):
    """Store shipyard modification fee."""
    context['shipyard_modification_fee'] = fee


@when('I create the shipyard')
def create_shipyard(context):
    """Create the shipyard."""
    context['shipyard'] = Shipyard(
        symbol=context['shipyard_symbol'],
        ship_types=context['shipyard_ship_types'],
        listings=[],
        transactions=[],
        modification_fee=context['shipyard_modification_fee']
    )


@then(parsers.parse('the shipyard should have symbol "{expected_symbol}"'))
def verify_shipyard_symbol(context, expected_symbol):
    """Verify the shipyard symbol."""
    assert context['shipyard'].symbol == expected_symbol


@then(parsers.parse('the shipyard should have ship types {expected_types}'))
def verify_shipyard_ship_types(context, expected_types):
    """Verify the shipyard ship types."""
    import ast
    expected = ast.literal_eval(expected_types)
    assert context['shipyard'].ship_types == expected


@then(parsers.parse('the shipyard should have modification fee {expected_fee:d}'))
def verify_shipyard_modification_fee(context, expected_fee):
    """Verify the shipyard modification fee."""
    assert context['shipyard'].modification_fee == expected_fee


# Shipyard with listings
@given(parsers.parse('the shipyard has a listing for "{ship_type}" priced at {price:d}'))
def shipyard_has_listing(context, ship_type, price):
    """Add a listing to the shipyard context."""
    if 'shipyard_listings' not in context:
        context['shipyard_listings'] = []

    listing = ShipListing(
        ship_type=ship_type,
        name=f"{ship_type} Ship",
        description=f"A {ship_type} vessel",
        purchase_price=price
    )
    context['shipyard_listings'].append(listing)


@when('I create the shipyard with listings')
def create_shipyard_with_listings(context):
    """Create the shipyard with listings."""
    ship_types = [listing.ship_type for listing in context['shipyard_listings']]
    context['shipyard'] = Shipyard(
        symbol=context['shipyard_symbol'],
        ship_types=ship_types,
        listings=context['shipyard_listings'],
        transactions=[],
        modification_fee=0
    )


@then(parsers.parse('the shipyard should have {count:d} listings'))
def verify_shipyard_listing_count(context, count):
    """Verify the number of listings."""
    assert len(context['shipyard'].listings) == count


@then(parsers.parse('the shipyard should have a listing for "{ship_type}"'))
def verify_shipyard_has_listing_for_type(context, ship_type):
    """Verify the shipyard has a listing for the ship type."""
    listing_types = [listing.ship_type for listing in context['shipyard'].listings]
    assert ship_type in listing_types


# Shipyard immutability
@given(parsers.parse('a created shipyard at waypoint "{waypoint_symbol}"'))
def created_shipyard(context, waypoint_symbol):
    """Create a shipyard."""
    context['shipyard'] = Shipyard(
        symbol=waypoint_symbol,
        ship_types=["SHIP_PROBE"],
        listings=[],
        transactions=[],
        modification_fee=1000
    )


@when('I attempt to modify the shipyard symbol')
def attempt_modify_shipyard(context):
    """Attempt to modify the shipyard."""
    try:
        context['shipyard'].symbol = "X1-DIFFERENT"
        context['modification_error'] = None
    except (FrozenInstanceError, AttributeError) as e:
        context['modification_error'] = e


# Empty listings
@given('the shipyard has no listings')
def shipyard_has_no_listings(context):
    """Set empty listings."""
    context['shipyard_listings'] = []


@when('I create the shipyard with empty listings')
def create_shipyard_with_empty_listings(context):
    """Create shipyard with empty listings."""
    context['shipyard'] = Shipyard(
        symbol=context['shipyard_symbol'],
        ship_types=[],
        listings=context['shipyard_listings'],
        transactions=[],
        modification_fee=0
    )
