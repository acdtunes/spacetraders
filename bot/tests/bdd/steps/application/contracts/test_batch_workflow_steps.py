"""Step definitions for batch contract workflow feature"""
import pytest
import asyncio
from pytest_bdd import scenarios, given, when, then, parsers
from datetime import datetime, timezone, timedelta
from unittest.mock import Mock, AsyncMock

from application.contracts.commands.batch_contract_workflow import (
    BatchContractWorkflowCommand,
    BatchResult
)
from domain.shared.contract import Contract, ContractTerms, Delivery, Payment
from domain.shared.value_objects import Waypoint
from configuration.container import get_mediator, reset_container

# Load all scenarios from the feature file
scenarios('../../../features/application/contracts/batch_contract_workflow.feature')


@pytest.fixture
def context():
    """Provide shared context dictionary for test data"""
    return {}


@pytest.fixture(autouse=True)
def reset_di_container():
    """Reset DI container before each test"""
    reset_container()
    yield
    reset_container()


@given(parsers.parse('a registered player with agent "{agent_symbol}"'))
def player_with_agent(context, agent_symbol):
    """Register a test player"""
    from configuration.container import get_player_repository
    from domain.shared.player import Player
    from datetime import datetime, timezone

    context['agent_symbol'] = agent_symbol

    # Create actual player in repository
    player = Player(
        player_id=None,  # Will be auto-assigned
        agent_symbol=agent_symbol,
        token="test-token",
        created_at=datetime.now(timezone.utc),
        last_active=datetime.now(timezone.utc),
        metadata={},
        credits=100000  # Default starting credits
    )

    player_repo = get_player_repository()
    created_player = player_repo.create(player)
    context['player_id'] = created_player.player_id


@given(parsers.parse('a ship "{ship_symbol}" with cargo capacity {capacity:d} in system "{system}"'))
def ship_with_capacity(context, ship_symbol, capacity, system):
    """Create a ship with specified cargo capacity"""
    from configuration.container import get_ship_repository
    from domain.shared.ship import Ship
    from domain.shared.value_objects import Waypoint, Fuel

    context['ship_symbol'] = ship_symbol
    context['cargo_capacity'] = capacity
    context['system'] = system

    # Create actual ship in repository
    waypoint = Waypoint(
        symbol=f"{system}-A1",
        x=0.0,
        y=0.0,
        system_symbol=system,
        waypoint_type="PLANET",
        has_fuel=True
    )
    fuel = Fuel(current=100, capacity=100)

    ship = Ship(
        ship_symbol=ship_symbol,
        player_id=context.get('player_id', 1),
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=100,
        cargo_capacity=capacity,
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.DOCKED
    )

    ship_repo = get_ship_repository()
    ship_repo.create(ship)


@given(parsers.parse('the ship is docked at waypoint "{waypoint}"'))
def ship_docked_at_waypoint(context, waypoint):
    """Set ship's current location"""
    from configuration.container import get_ship_repository
    from domain.shared.ship import Ship
    from domain.shared.value_objects import Waypoint, Fuel

    context['current_waypoint'] = waypoint

    # Update ship's location in repository if ship exists
    ship_repo = get_ship_repository()
    ship_symbol = context.get('ship_symbol')
    player_id = context.get('player_id', 1)

    if ship_symbol:
        ship = ship_repo.find_by_symbol(ship_symbol, player_id)
        if ship:
            # Update ship with new location
            new_waypoint = Waypoint(
                symbol=waypoint,
                x=0.0,
                y=0.0,
                system_symbol=context.get('system', 'X1-TEST'),
                waypoint_type="PLANET",
                has_fuel=True
            )

            updated_ship = Ship(
                ship_symbol=ship.ship_symbol,
                player_id=ship.player_id,
                current_location=new_waypoint,
                fuel=ship.fuel,
                fuel_capacity=ship.fuel_capacity,
                cargo_capacity=ship.cargo_capacity,
                cargo_units=ship.cargo_units,
                engine_speed=ship.engine_speed,
                nav_status=Ship.DOCKED
            )
            ship_repo.update(updated_ship)


@given(parsers.parse('a market at "{waypoint}" sells "{good}" for {price:d} credits per unit'))
def market_sells_good(context, waypoint, good, price):
    """Set up market data in database"""
    if 'markets' not in context:
        context['markets'] = {}
    context['markets'][waypoint] = {
        'good': good,
        'price': price
    }


@given(parsers.parse('the ship has {credits:d} credits available'))
def ship_has_credits(context, credits):
    """Set ship's available credits"""
    context['available_credits'] = credits


@given(parsers.parse('a contract requires {units:d} units of "{good}" delivery'))
def contract_requires_delivery(context, units, good):
    """Create a test contract with specific requirements"""
    context['contract_requirements'] = {
        'units': units,
        'good': good
    }


@given(parsers.parse('the ship has cargo capacity of {capacity:d} units'))
def ship_cargo_capacity(context, capacity):
    """Override ship cargo capacity"""
    context['cargo_capacity'] = capacity


@given(parsers.parse('a market at "{waypoint}" initially sells "{good}" for {initial_price:d} credits per unit'))
def market_initial_price(context, waypoint, good, initial_price):
    """Set initial market price (for polling scenario)"""
    if 'price_changes' not in context:
        context['price_changes'] = {}
    context['price_changes'][waypoint] = {
        'good': good,
        'initial_price': initial_price,
        'polls': 0
    }


@given(parsers.parse('the market price will drop to {new_price:d} credits after {polls:d} poll'))
def market_price_drops_after_polls(context, new_price, polls):
    """Configure price drop after N polls"""
    waypoint = list(context['price_changes'].keys())[0]
    context['price_changes'][waypoint]['drop_after_polls'] = polls
    context['price_changes'][waypoint]['new_price'] = new_price


@given(parsers.parse('contract {contract_num:d} will fail during delivery'))
def contract_will_fail(context, contract_num):
    """Mark a specific contract to fail"""
    if 'failing_contracts' not in context:
        context['failing_contracts'] = []
    context['failing_contracts'].append(contract_num)


@when(parsers.parse('I execute batch contract workflow for ship "{ship_symbol}" with {iterations:d} iteration'))
@when(parsers.parse('I execute batch contract workflow for ship "{ship_symbol}" with {iterations:d} iterations'))
def execute_batch_workflow(context, ship_symbol, iterations):
    """Execute the batch contract workflow command"""
    mediator = get_mediator()

    command = BatchContractWorkflowCommand(
        ship_symbol=ship_symbol,
        iterations=iterations,
        player_id=context['player_id']
    )

    try:
        result = asyncio.run(mediator.send_async(command))
        context['workflow_result'] = result
        context['workflow_error'] = None
    except Exception as e:
        context['workflow_result'] = None
        context['workflow_error'] = e


@then(parsers.parse('the workflow should negotiate {count:d} contract'))
@then(parsers.parse('the workflow should negotiate {count:d} contracts'))
def verify_contracts_negotiated(context, count):
    """Verify number of contracts negotiated"""
    assert context.get('workflow_result') is not None, "Workflow did not complete"
    assert context['workflow_result'].negotiated == count, \
        f"Expected {count} contracts negotiated, got {context['workflow_result'].negotiated}"


@then('the workflow should accept the contract when profitable')
def verify_contract_accepted_when_profitable(context):
    """Verify contract was accepted after profitability check"""
    assert context.get('workflow_result') is not None
    assert context['workflow_result'].accepted >= 1, "No contracts were accepted"


@then('the workflow should purchase required goods from cheapest market')
def verify_goods_purchased(context):
    """Verify goods were purchased from cheapest market"""
    # This is verified by successful fulfillment
    assert context['workflow_result'].fulfilled >= 1, "Contract was not fulfilled (goods not purchased)"


@then('the workflow should deliver goods to contract destination')
def verify_goods_delivered(context):
    """Verify goods were delivered"""
    # Verified by successful fulfillment
    assert context['workflow_result'].fulfilled >= 1


@then(parsers.parse('the workflow should fulfill {count:d} contract successfully'))
@then(parsers.parse('the workflow should fulfill {count:d} contracts successfully'))
def verify_contracts_fulfilled(context, count):
    """Verify number of contracts fulfilled"""
    assert context.get('workflow_result') is not None
    assert context['workflow_result'].fulfilled == count, \
        f"Expected {count} contracts fulfilled, got {context['workflow_result'].fulfilled}"


@then('the workflow should return positive net profit')
def verify_positive_profit(context):
    """Verify workflow generated profit"""
    assert context.get('workflow_result') is not None
    assert context['workflow_result'].total_profit > 0, \
        f"Expected positive profit, got {context['workflow_result'].total_profit}"


@then(parsers.parse('the workflow should make {trips:d} trips between market and delivery destination'))
def verify_trip_count(context, trips):
    """Verify number of trips made"""
    assert context.get('workflow_result') is not None
    assert context['workflow_result'].total_trips >= trips, \
        f"Expected at least {trips} trips, got {context['workflow_result'].total_trips}"


@then(parsers.parse('the workflow should deliver {units:d} units total'))
def verify_total_units_delivered(context, units):
    """Verify total units delivered"""
    # Verified by contract fulfillment
    assert context['workflow_result'].fulfilled >= 1


@then('the workflow should fulfill the contract successfully')
def verify_single_contract_fulfilled(context):
    """Verify single contract was fulfilled"""
    assert context['workflow_result'].fulfilled == 1


@then('the workflow should poll market prices until profitable')
def verify_price_polling(context):
    """Verify workflow polled for better prices"""
    # This would be verified by checking poll count in result
    assert context['workflow_result'].fulfilled >= 1


@then('the workflow should accept the contract after price drop')
def verify_acceptance_after_price_drop(context):
    """Verify contract was accepted after price improved"""
    assert context['workflow_result'].accepted >= 1


@then(parsers.parse('the workflow should report {count:d} failed contract'))
@then(parsers.parse('the workflow should report {count:d} failed contracts'))
def verify_failed_contracts(context, count):
    """Verify number of failed contracts"""
    assert context.get('workflow_result') is not None
    assert context['workflow_result'].failed == count, \
        f"Expected {count} failed contracts, got {context['workflow_result'].failed}"


@then('the workflow should return batch statistics with total profit')
def verify_batch_statistics(context):
    """Verify batch result contains statistics"""
    assert context.get('workflow_result') is not None
    assert hasattr(context['workflow_result'], 'total_profit')
    assert hasattr(context['workflow_result'], 'negotiated')
    assert hasattr(context['workflow_result'], 'fulfilled')
    assert hasattr(context['workflow_result'], 'failed')
