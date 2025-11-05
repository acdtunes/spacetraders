"""Step definitions for GetShipyardListings query feature."""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock
import requests
import asyncio

from application.shipyard.queries.get_shipyard_listings import (
    GetShipyardListingsQuery,
    GetShipyardListingsHandler
)
from domain.shared.exceptions import ShipyardNotFoundError

# Load scenarios
scenarios('../../../features/application/shipyard/get_shipyard_listings.feature')


# Fixtures
@pytest.fixture
def context():
    """Shared context for steps."""
    return {}


@pytest.fixture
def mock_api():
    """Mock API client."""
    return Mock()


@pytest.fixture
def handler(mock_api):
    """Create handler with mock API."""
    # Factory function that returns the mock API for any player_id
    def api_factory(player_id):
        return mock_api
    return GetShipyardListingsHandler(api_client_factory=api_factory)


# Given steps
@given(parsers.parse('I have a player with ID {player_id:d}'))
def player_with_id(context, player_id):
    """Store player ID."""
    context['player_id'] = player_id


@given(parsers.parse('the API returns a shipyard at "{waypoint}" with ships:'))
def api_returns_shipyard_with_ships(context, waypoint, mock_api):
    """Mock API to return shipyard data."""
    # Parse the table data from the scenario
    ship_listings = [
        {
            "type": "SHIP_MINING_DRONE",
            "name": "Mining Drone",
            "description": "A small automated mining vessel",
            "purchasePrice": 50000
        },
        {
            "type": "SHIP_PROBE",
            "name": "Probe Satellite",
            "description": "A reconnaissance satellite",
            "purchasePrice": 25000
        }
    ]

    mock_api.get_shipyard.return_value = {
        "data": {
            "symbol": waypoint,
            "shipTypes": [{"type": "SHIP_MINING_DRONE"}, {"type": "SHIP_PROBE"}],
            "ships": ship_listings,
            "transactions": [],
            "modificationsFee": 1000
        }
    }
    context['mock_api'] = mock_api


@given(parsers.parse('the API returns a 404 error for shipyard at "{waypoint}"'))
def api_returns_404(context, waypoint, mock_api):
    """Mock API to return 404 error."""
    error = requests.HTTPError()
    error.response = Mock()
    error.response.status_code = 404
    mock_api.get_shipyard.side_effect = error
    context['mock_api'] = mock_api


@given(parsers.parse('the API returns a shipyard at "{waypoint}" with no ships'))
def api_returns_empty_shipyard(context, waypoint, mock_api):
    """Mock API to return empty shipyard."""
    mock_api.get_shipyard.return_value = {
        "data": {
            "symbol": waypoint,
            "shipTypes": [],
            "ships": [],
            "transactions": [],
            "modificationsFee": 0
        }
    }
    context['mock_api'] = mock_api


@given(parsers.parse('the API returns a 400 error for invalid system "{system_symbol}"'))
def api_returns_400(context, system_symbol, mock_api):
    """Mock API to return 400 error."""
    error = requests.HTTPError()
    error.response = Mock()
    error.response.status_code = 400
    mock_api.get_shipyard.side_effect = error
    context['mock_api'] = mock_api


# When steps
@when(parsers.parse('I query shipyard listings for system "{system_symbol}" and waypoint "{waypoint_symbol}" as player {player_id:d}'))
def query_shipyard_listings(context, system_symbol, waypoint_symbol, player_id, handler):
    """Execute the query."""
    query = GetShipyardListingsQuery(
        system_symbol=system_symbol,
        waypoint_symbol=waypoint_symbol,
        player_id=player_id
    )

    async def run_query():
        try:
            context['result'] = await handler.handle(query)
            context['error'] = None
        except Exception as e:
            context['error'] = e
            context['result'] = None

    # Run the async query
    asyncio.run(run_query())


# Then steps
@then('the query should succeed')
def query_should_succeed(context):
    """Verify the query succeeded."""
    assert context['error'] is None, f"Expected success but got error: {context['error']}"
    assert context['result'] is not None


@then(parsers.parse('the query should fail with "{error_type}"'))
def query_should_fail_with_error(context, error_type):
    """Verify the query failed with specific error."""
    assert context['error'] is not None, "Expected error but query succeeded"
    if error_type == "ShipyardNotFoundError":
        assert isinstance(context['error'], ShipyardNotFoundError)


@then(parsers.parse('the query should fail with error status {status_code:d}'))
def query_should_fail_with_status(context, status_code):
    """Verify the query failed with specific HTTP status."""
    assert context['error'] is not None, "Expected error but query succeeded"
    assert isinstance(context['error'], requests.HTTPError)
    assert context['error'].response.status_code == status_code


@then(parsers.parse('the shipyard should have symbol "{expected_symbol}"'))
def verify_shipyard_symbol(context, expected_symbol):
    """Verify shipyard symbol."""
    assert context['result'].symbol == expected_symbol


@then(parsers.parse('the shipyard should have {count:d} listings'))
def verify_listing_count(context, count):
    """Verify number of listings."""
    assert len(context['result'].listings) == count


@then(parsers.parse('the shipyard should have a listing for "{ship_type}" priced at {price:d}'))
def verify_listing_exists(context, ship_type, price):
    """Verify a specific listing exists."""
    listings = context['result'].listings
    matching = [l for l in listings if l.ship_type == ship_type and l.purchase_price == price]
    assert len(matching) > 0, f"No listing found for {ship_type} at price {price}"
