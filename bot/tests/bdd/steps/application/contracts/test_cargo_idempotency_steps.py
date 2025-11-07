"""Step definitions for contract workflow cargo idempotency tests"""
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, AsyncMock, patch

from domain.shared.player import Player
from domain.shared.ship import Ship
from domain.shared.value_objects import Waypoint, Fuel, CargoItem, Cargo
from domain.shared.contract import Contract, ContractTerms, Delivery, Payment
from application.contracts.commands.batch_contract_workflow import (
    BatchContractWorkflowCommand,
    BatchContractWorkflowHandler
)
from datetime import datetime, timezone

# Load scenarios from feature file
scenarios('../../../features/application/contracts/cargo_idempotency.feature')


# Background steps

@given(parsers.parse('a player with id {player_id:d} and {credits:d} credits'))
def given_player(context, player_id, credits):
    """Create a player"""
    context['player'] = Player(
        player_id=player_id,
        agent_symbol=f"TEST-AGENT-{player_id}",
        token="TEST-TOKEN",
        created_at=datetime.now(timezone.utc),
        credits=credits
    )


@given(parsers.parse('a ship "{ship_symbol}" for player {player_id:d} at waypoint "{waypoint_symbol}"'))
def given_ship_at_waypoint(context, ship_symbol, player_id, waypoint_symbol):
    """Create a ship at waypoint"""
    waypoint = Waypoint(symbol=waypoint_symbol, x=0, y=0)
    fuel = Fuel(current=100, capacity=100)

    # Start with empty cargo by default
    cargo = Cargo(capacity=0, units=0, inventory=())

    context['ship'] = Ship(
        ship_symbol=ship_symbol,
        player_id=player_id,
        current_location=waypoint,
        fuel=fuel,
        fuel_capacity=100,
        cargo_capacity=0,  # Will be set in next step
        cargo_units=0,
        engine_speed=30,
        nav_status=Ship.DOCKED
    )
    context['ship']._cargo = cargo  # Set cargo directly


@given(parsers.parse('ship "{ship_symbol}" has {capacity:d} cargo capacity'))
def given_ship_cargo_capacity(context, ship_symbol, capacity):
    """Set ship cargo capacity"""
    # Update ship's cargo capacity
    context['ship']._cargo_capacity = capacity
    # Update cargo object with new capacity
    current_cargo = context['ship']._cargo
    context['ship']._cargo = Cargo(
        capacity=capacity,
        units=current_cargo.units,
        inventory=current_cargo.inventory
    )


@given(parsers.parse('a mock contract requiring {units:d} units of "{trade_symbol}" to deliver to "{destination}"'))
def given_mock_contract(context, units, trade_symbol, destination):
    """Create a mock contract"""
    destination_waypoint = Waypoint(symbol=destination, x=10, y=10)

    delivery = Delivery(
        trade_symbol=trade_symbol,
        destination=destination_waypoint,
        units_required=units,
        units_fulfilled=0
    )

    payment = Payment(
        on_accepted=10000,
        on_fulfilled=50000
    )

    deadline = datetime(2025, 12, 31, 0, 0, 0, tzinfo=timezone.utc)

    terms = ContractTerms(
        deadline=deadline,
        payment=payment,
        deliveries=[delivery]
    )

    context['contract'] = Contract(
        contract_id="CONTRACT-1",
        faction_symbol="COSMIC",
        type="PROCUREMENT",
        terms=terms,
        accepted=False,
        fulfilled=False,
        deadline_to_accept=deadline
    )


@given(parsers.parse('waypoint "{waypoint_symbol}" sells "{trade_symbol}" at {price:d} credits per unit'))
def given_waypoint_sells_cargo(context, waypoint_symbol, trade_symbol, price):
    """Define that waypoint sells cargo at price"""
    context['seller_waypoint'] = waypoint_symbol
    context['trade_symbol'] = trade_symbol
    context['sell_price'] = price


@given(parsers.parse('ship "{ship_symbol}" has {units:d} units of "{trade_symbol}" in cargo'))
def given_ship_has_cargo(context, ship_symbol, units, trade_symbol):
    """Add cargo to ship"""
    current_cargo = context['ship']._cargo

    # Create new cargo item
    new_item = CargoItem(
        symbol=trade_symbol,
        name=trade_symbol.replace('_', ' ').title(),
        description=f"{trade_symbol} description",
        units=units
    )

    # Add to existing inventory
    new_inventory = list(current_cargo.inventory)

    # Check if item already exists, update or add
    found = False
    for i, item in enumerate(new_inventory):
        if item.symbol == trade_symbol:
            new_inventory[i] = CargoItem(
                symbol=item.symbol,
                name=item.name,
                description=item.description,
                units=item.units + units
            )
            found = True
            break

    if not found:
        new_inventory.append(new_item)

    # Calculate new total units
    new_total_units = sum(item.units for item in new_inventory)

    # Create new Cargo object
    context['ship']._cargo = Cargo(
        capacity=current_cargo.capacity,
        units=new_total_units,
        inventory=tuple(new_inventory)
    )


@given(parsers.parse('ship "{ship_symbol}" has empty cargo'))
def given_ship_empty_cargo(context, ship_symbol):
    """Ensure ship has empty cargo"""
    current_cargo = context['ship']._cargo
    context['ship']._cargo = Cargo(
        capacity=current_cargo.capacity,
        units=0,
        inventory=()
    )


# When steps

@when(parsers.parse('I execute contract batch workflow for {iterations:d} iteration'))
def execute_batch_workflow(context, iterations):
    """Execute batch contract workflow"""
    # Initialize tracking lists
    context['mock_calls'] = []
    context['jettison_calls'] = []
    context['navigate_calls'] = []
    context['purchase_calls'] = []

    # Create mocks
    mock_mediator = Mock()
    mock_ship_repository = Mock()

    # Setup ship repository
    mock_ship_repository.find_by_symbol.return_value = context['ship']

    # Track calls to different commands
    async def mock_send_async(command):
        context['mock_calls'].append(command)

        # Track specific command types
        command_type = type(command).__name__

        if 'JettisonCargo' in command_type:
            context['jettison_calls'].append({
                'symbol': command.cargo_symbol,
                'units': command.units
            })
            # Update ship cargo after jettison
            ship = context['ship']
            current_cargo = ship._cargo
            new_inventory = []
            for item in current_cargo.inventory:
                if item.symbol == command.cargo_symbol:
                    remaining = item.units - command.units
                    if remaining > 0:
                        new_inventory.append(CargoItem(
                            symbol=item.symbol,
                            name=item.name,
                            description=item.description,
                            units=remaining
                        ))
                else:
                    new_inventory.append(item)
            new_total_units = sum(item.units for item in new_inventory)
            ship._cargo = Cargo(
                capacity=current_cargo.capacity,
                units=new_total_units,
                inventory=tuple(new_inventory)
            )
        elif 'NavigateShip' in command_type:
            context['navigate_calls'].append({
                'destination': command.destination_symbol
            })
        elif 'PurchaseCargo' in command_type:
            context['purchase_calls'].append({
                'symbol': command.trade_symbol,
                'units': command.units
            })
        elif 'SyncShips' in command_type:
            # Return ship with current state (no-op for tests)
            pass
        elif 'GetActiveContracts' in command_type:
            return []  # No active contracts initially
        elif 'NegotiateContract' in command_type:
            return context['contract']
        elif 'EvaluateContractProfitability' in command_type:
            # Mock profitability result
            profitability = Mock()
            profitability.is_profitable = True
            profitability.reason = "Test profitable"
            profitability.net_profit = 10000
            profitability.cheapest_market_waypoint = context.get('seller_waypoint', 'X1-TEST-A1')
            return profitability
        elif 'AcceptContract' in command_type:
            context['contract']._accepted = True
        elif 'DeliverContract' in command_type:
            pass  # Mock delivery
        elif 'FulfillContract' in command_type:
            context['contract']._fulfilled = True
        elif 'DockShip' in command_type:
            pass  # Mock dock

        return None

    mock_mediator.send_async = AsyncMock(side_effect=mock_send_async)

    # Create handler
    handler = BatchContractWorkflowHandler(
        mediator=mock_mediator,
        ship_repository=mock_ship_repository
    )

    # Execute workflow
    command = BatchContractWorkflowCommand(
        ship_symbol=context['ship'].ship_symbol,
        iterations=iterations,
        player_id=context['player'].player_id
    )

    import asyncio
    context['result'] = asyncio.run(handler.handle(command))


# Then steps - workflow behavior checks

@then('the workflow should skip navigation to seller market')
def check_no_navigation(context):
    """Verify no navigation to seller market occurred"""
    # Check that no navigate commands were called
    assert len(context['navigate_calls']) == 0 or \
           all(call['destination'] != context.get('seller_waypoint')
               for call in context['navigate_calls'][:1])  # Check only first navigate (to seller)


@then('the workflow should skip cargo purchase')
def check_no_purchase(context):
    """Verify no cargo purchase occurred"""
    assert len(context['purchase_calls']) == 0


@then('the workflow should deliver cargo directly')
def check_delivery_occurred(context):
    """Verify delivery occurred"""
    # Contract should be fulfilled
    assert context['contract'].fulfilled


@then('the contract should be fulfilled')
def check_contract_fulfilled(context):
    """Verify contract was fulfilled"""
    assert context['contract'].fulfilled


@then(parsers.parse('the workflow should jettison {units:d} units of "{trade_symbol}"'))
def check_jettison_occurred(context, units, trade_symbol):
    """Verify jettison occurred"""
    jettison_found = False
    for call in context['jettison_calls']:
        if call['symbol'] == trade_symbol and call['units'] == units:
            jettison_found = True
            break
    assert jettison_found, f"Expected jettison of {units} units of {trade_symbol}, but found: {context['jettison_calls']}"


@then(parsers.parse('the workflow should navigate to seller market "{waypoint_symbol}"'))
def check_navigation_to_seller(context, waypoint_symbol):
    """Verify navigation to seller market"""
    navigate_found = False
    for call in context['navigate_calls']:
        if call['destination'] == waypoint_symbol:
            navigate_found = True
            break
    assert navigate_found, f"Expected navigation to {waypoint_symbol}, but found: {context['navigate_calls']}"


@then(parsers.parse('the workflow should purchase {units:d} units of "{trade_symbol}"'))
def check_purchase_occurred(context, units, trade_symbol):
    """Verify purchase occurred"""
    purchase_found = False
    for call in context['purchase_calls']:
        if call['symbol'] == trade_symbol and call['units'] == units:
            purchase_found = True
            break
    assert purchase_found, f"Expected purchase of {units} units of {trade_symbol}, but found: {context['purchase_calls']}"


@then('the workflow should not jettison any cargo')
def check_no_jettison(context):
    """Verify no jettison occurred"""
    assert len(context['jettison_calls']) == 0
