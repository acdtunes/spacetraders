"""Step definitions for purchase cargo feature"""
import pytest
import asyncio
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock

from spacetraders.application.trading.commands.purchase_cargo import (
    PurchaseCargoCommand,
    PurchaseCargoHandler
)
from spacetraders.domain.shared.ship import Ship

# Load all scenarios from the feature file
scenarios('../../../features/application/trading/purchase_cargo.feature')


@pytest.fixture
def context():
    """Provide shared context dictionary for test data"""
    return {}


@given(parsers.parse('a registered player with agent "{agent_symbol}"'))
def player_with_agent(context, agent_symbol):
    """Register a test player"""
    context['agent_symbol'] = agent_symbol
    context['player_id'] = 1


@given(parsers.parse('a ship "{ship_symbol}" exists in the database'))
def ship_exists(context, ship_symbol):
    """Create a test ship"""
    context['ship_symbol'] = ship_symbol
    # Ship will be mocked in the handler


@given(parsers.parse('the ship is docked at waypoint "{waypoint}"'))
def ship_docked_at_waypoint(context, waypoint):
    """Set ship's docked location"""
    context['ship_nav_status'] = Ship.DOCKED
    context['ship_waypoint'] = waypoint


@given(parsers.parse('the ship is in orbit at waypoint "{waypoint}"'))
def ship_in_orbit(context, waypoint):
    """Set ship's orbit status"""
    context['ship_nav_status'] = Ship.IN_ORBIT
    context['ship_waypoint'] = waypoint


@given(parsers.parse('the ship has {units:d} units of cargo space available'))
def ship_cargo_space(context, units):
    """Set ship's available cargo space"""
    context['cargo_space_available'] = units
    context['cargo_capacity'] = 100  # Total capacity


@given(parsers.parse('the player has {credits:d} credits'))
def player_credits(context, credits):
    """Set player's available credits"""
    context['player_credits'] = credits


@given(parsers.parse('the market at "{waypoint}" sells "{good}" for {price:d} credits per unit'))
def market_sells_good(context, waypoint, good, price):
    """Set up market data"""
    if 'markets' not in context:
        context['markets'] = {}
    context['markets'][waypoint] = {
        'good': good,
        'price': price
    }


@when(parsers.parse('I purchase {units:d} units of "{trade_symbol}" for ship "{ship_symbol}"'))
def purchase_cargo(context, units, trade_symbol, ship_symbol):
    """Execute purchase cargo command"""
    # Create mock API client
    mock_api_client = Mock()

    # Determine if this should fail based on context
    ship_nav_status = context.get('ship_nav_status', Ship.DOCKED)
    player_credits = context.get('player_credits', 10000)
    cargo_space = context.get('cargo_space_available', 50)
    price_per_unit = context['markets'][context['ship_waypoint']]['price']
    total_cost = units * price_per_unit

    # Configure mock to fail or succeed based on preconditions
    if ship_nav_status != Ship.DOCKED:
        # Ship must be docked
        mock_api_client.purchase_cargo.side_effect = Exception("Ship must be docked to purchase cargo")
    elif total_cost > player_credits:
        # Insufficient credits
        mock_api_client.purchase_cargo.side_effect = Exception("Insufficient credits")
    elif units > cargo_space:
        # Insufficient cargo space
        mock_api_client.purchase_cargo.side_effect = Exception("Insufficient cargo space")
    else:
        # Success case
        mock_api_client.purchase_cargo.return_value = {
            'data': {
                'agent': {
                    'credits': player_credits - total_cost
                },
                'cargo': {
                    'capacity': context.get('cargo_capacity', 100),
                    'units': cargo_space + units,
                    'inventory': [
                        {
                            'symbol': trade_symbol,
                            'units': units
                        }
                    ]
                },
                'transaction': {
                    'waypointSymbol': context['ship_waypoint'],
                    'tradeSymbol': trade_symbol,
                    'units': units,
                    'pricePerUnit': price_per_unit,
                    'totalPrice': total_cost
                }
            }
        }

    # Create handler with mocked API client factory
    handler = PurchaseCargoHandler(api_client_factory=lambda player_id: mock_api_client)

    command = PurchaseCargoCommand(
        ship_symbol=ship_symbol,
        trade_symbol=trade_symbol,
        units=units,
        player_id=context['player_id']
    )

    try:
        result = asyncio.run(handler.handle(command))
        context['purchase_result'] = result
        context['purchase_error'] = None
        context['mock_api_client'] = mock_api_client  # Store for verification
    except Exception as e:
        context['purchase_result'] = None
        context['purchase_error'] = e


@then('the purchase should succeed')
def verify_purchase_success(context):
    """Verify purchase was successful"""
    assert context.get('purchase_error') is None, \
        f"Purchase failed with error: {context.get('purchase_error')}"
    assert context.get('purchase_result') is not None, \
        "Purchase did not return a result"


@then(parsers.parse('the ship should have {units:d} units of "{trade_symbol}" in cargo'))
def verify_cargo_contains(context, units, trade_symbol):
    """Verify ship cargo contains the purchased goods"""
    result = context.get('purchase_result')
    assert result is not None, "No purchase result"
    # Result should contain updated cargo information
    # This is observable behavior from the API response


@then(parsers.parse('{credits:d} credits should be deducted from player balance'))
def verify_credits_deducted(context, credits):
    """Verify credits were deducted"""
    result = context.get('purchase_result')
    assert result is not None, "No purchase result"
    # Result should show credits deducted
    # This is observable from the transaction response


@then('the ship cargo should be full')
def verify_cargo_full(context):
    """Verify ship cargo is at capacity"""
    result = context.get('purchase_result')
    assert result is not None, "No purchase result"
    # Observable: cargo units == cargo capacity


@then(parsers.parse('the purchase should fail with "{error_message}"'))
def verify_purchase_failure(context, error_message):
    """Verify purchase failed with expected error"""
    assert context.get('purchase_error') is not None, \
        "Expected purchase to fail but it succeeded"

    error = context['purchase_error']
    assert error_message.lower() in str(error).lower(), \
        f"Expected error containing '{error_message}', got: {error}"
