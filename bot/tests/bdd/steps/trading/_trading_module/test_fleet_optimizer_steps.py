"""
BDD Step Definitions for Fleet Trade Optimizer

Tests for multi-ship conflict avoidance and route optimization.
"""

import logging
from unittest.mock import Mock, MagicMock
import pytest
from pytest_bdd import scenarios, given, when, then, parsers

from spacetraders_bot.operations._trading import (
    FleetTradeOptimizer, MultiLegRoute, RouteSegment, TradeAction,
    MultiLegTradeOptimizer
)

# Load the feature file
scenarios('../../../../bdd/features/trading/_trading_module/fleet_optimizer.feature')

# NOTE: Background steps (test database, API client, markets, trade opportunities)
# are defined in conftest.py and shared across all _trading_module tests

# ===========================
# Fleet Optimizer Setup
# ===========================

@given(parsers.parse('a fleet optimizer for player {player_id:d}'))
def create_fleet_optimizer(context, player_id, mock_logger):
    """Create fleet optimizer instance"""
    context['player_id'] = player_id
    context['logger'] = mock_logger

    # Initialize ships list if not exists
    if 'ships' not in context:
        context['ships'] = []

    # Create optimizer
    optimizer = FleetTradeOptimizer(
        api=context['api'],
        db=context['database'],
        player_id=player_id,
        logger=mock_logger
    )

    # CRITICAL FIX: FleetTradeOptimizer creates MultiLegTradeOptimizer internally
    # and calls _get_markets_in_system and _get_trade_opportunities on IT.
    # We need to mock those methods on the MultiLegTradeOptimizer CLASS, not the instance.

    # Mock MultiLegTradeOptimizer methods to use test data
    # Use lambda to evaluate context at call time, not at mock creation time
    MultiLegTradeOptimizer._get_markets_in_system = Mock(side_effect=lambda system: context.get('markets', []))
    MultiLegTradeOptimizer._get_trade_opportunities = Mock(side_effect=lambda system, markets: context.get('trade_opportunities', []))

    # Mock _find_ship_route to return realistic routes based on opportunities
    def mock_find_ship_route(start_waypoint, markets, trade_opportunities, max_stops,
                            cargo_capacity, starting_credits, ship_speed, starting_cargo=None):
        if not trade_opportunities:
            return None

        # Prefer opportunities starting at current location, then highest spread
        # This makes tests deterministic based on ship starting positions
        local_opportunities = [opp for opp in trade_opportunities if opp['buy_waypoint'] == start_waypoint]

        if local_opportunities:
            # Pick best local opportunity
            best_opp = max(local_opportunities, key=lambda x: x['spread'])
        else:
            # Fall back to best overall opportunity
            best_opp = max(trade_opportunities, key=lambda x: x['spread'])

        # Calculate units to buy (limited by cargo capacity and existing cargo)
        used_cargo = sum(starting_cargo.values()) if starting_cargo else 0
        available_capacity = cargo_capacity - used_cargo
        units = min(best_opp['trade_volume'], available_capacity)

        if units <= 0:
            return None

        # Calculate profit
        profit = units * best_opp['spread']

        if profit <= 0:
            return None

        # Create route with BUY and SELL actions
        buy_action = TradeAction(
            waypoint=best_opp['buy_waypoint'],
            good=best_opp['good'],
            action='BUY',
            units=units,
            price_per_unit=best_opp['buy_price'],
            total_value=units * best_opp['buy_price']
        )

        sell_action = TradeAction(
            waypoint=best_opp['sell_waypoint'],
            good=best_opp['good'],
            action='SELL',
            units=units,
            price_per_unit=best_opp['sell_price'],
            total_value=units * best_opp['sell_price']
        )

        # Calculate segment state
        cargo_after = {best_opp['good']: 0}  # Sold everything
        cost = units * best_opp['buy_price']
        revenue = units * best_opp['sell_price']
        credits_after = starting_credits - cost + revenue

        segment = RouteSegment(
            from_waypoint=best_opp['buy_waypoint'],
            to_waypoint=best_opp['sell_waypoint'],
            distance=100,  # Mock distance
            fuel_cost=10,
            actions_at_destination=[buy_action, sell_action],
            cargo_after=cargo_after,
            credits_after=credits_after,
            cumulative_profit=profit
        )

        route = MultiLegRoute(
            segments=[segment],
            total_profit=profit,
            total_distance=100,
            total_fuel_cost=10,
            estimated_time_minutes=5
        )

        return route

    optimizer._find_ship_route = Mock(side_effect=mock_find_ship_route)

    context['fleet_optimizer'] = optimizer


@given(parsers.parse('ship "{ship}" at "{waypoint}" with cargo capacity {capacity:d}'))
def setup_ship(context, ship, waypoint, capacity):
    """Setup ship with location and cargo capacity"""
    ship_data = {
        'symbol': ship,
        'nav': {
            'waypointSymbol': waypoint,
            'systemSymbol': context.get('system', 'X1-TEST')
        },
        'cargo': {
            'capacity': capacity,
            'units': 0,
            'inventory': []
        },
        'fuel': {
            'capacity': 1000,
            'current': 1000
        },
        'engine': {
            'speed': 30
        }
    }
    context['ships'].append(ship_data)


@given(parsers.parse('ship "{ship}" has {units:d} units of "{good}" in cargo'))
def add_ship_cargo(context, ship, units, good):
    """Add cargo to ship's inventory"""
    for ship_data in context['ships']:
        if ship_data['symbol'] == ship:
            ship_data['cargo']['inventory'].append({
                'symbol': good,
                'units': units
            })
            ship_data['cargo']['units'] += units
            break


@given('only one profitable trade opportunity exists')
def limit_to_one_opportunity(context):
    """Limit trade opportunities to just one"""
    if context.get('trade_opportunities'):
        context['trade_opportunities'] = context['trade_opportunities'][:1]


@given('all opportunities become unprofitable after first ship assignment')
def make_opportunities_unprofitable_after_first(context):
    """Make all opportunities unprofitable after first assignment"""
    # Set a flag that will be checked during route finding
    context['unprofitable_after_first'] = True

    # Override the mock to handle this
    original_find_route = context['fleet_optimizer']._find_ship_route
    call_count = [0]

    def conditional_find_route(*args, **kwargs):
        call_count[0] += 1
        if call_count[0] > 1:
            # After first ship, return None (no profitable route)
            return None
        return original_find_route(*args, **kwargs)

    context['fleet_optimizer']._find_ship_route = Mock(side_effect=conditional_find_route)


@given('no profitable trade opportunities exist')
def no_opportunities(context):
    """Clear all trade opportunities"""
    context['trade_opportunities'] = []
    context['fleet_optimizer']._get_trade_opportunities = Mock(return_value=[])


# ===========================
# Action Steps
# ===========================

@when(parsers.parse('I optimize fleet routes for {ship_count:d} ships with max {max_stops:d} stops'))
def optimize_fleet_routes(context, ship_count, max_stops):
    """Run fleet optimization (plural ships)"""
    _run_fleet_optimization(context, ship_count, max_stops)


@when(parsers.parse('I optimize fleet routes for {ship_count:d} ship with max {max_stops:d} stops'))
def optimize_fleet_routes_singular(context, ship_count, max_stops):
    """Run fleet optimization (singular ship)"""
    _run_fleet_optimization(context, ship_count, max_stops)


def _run_fleet_optimization(context, ship_count, max_stops):
    """Common fleet optimization logic"""
    ships = context['ships'][:ship_count]
    system = context.get('system', 'X1-TEST')
    starting_credits = 100000

    result = context['fleet_optimizer'].optimize_fleet(
        ships=ships,
        system=system,
        max_stops=max_stops,
        starting_credits=starting_credits
    )

    context['fleet_result'] = result

    # Store individual ship routes for easy access
    if result:
        context['ship_routes'] = result.get('ship_routes', {})
        context['reserved_pairs'] = result.get('reserved_pairs', set())
    else:
        context['ship_routes'] = {}
        context['reserved_pairs'] = set()


# ===========================
# Assertion Steps
# ===========================

@then(parsers.parse('ship "{ship}" should have a profitable route'))
def check_ship_has_profitable_route(context, ship):
    """Verify ship has a profitable route assigned"""
    ship_routes = context['ship_routes']
    assert ship in ship_routes, f"Ship {ship} has no route assigned"

    route = ship_routes[ship]
    assert route.total_profit > 0, f"Ship {ship} route is not profitable: {route.total_profit}"


@then(parsers.parse('ship "{ship}" should have no route assigned'))
def check_ship_has_no_route(context, ship):
    """Verify ship has no route assigned"""
    ship_routes = context['ship_routes']
    assert ship not in ship_routes, f"Ship {ship} should not have a route"


@then('the routes should have no resource conflicts')
def check_no_resource_conflicts(context):
    """Verify no (resource, waypoint) conflicts between ships"""
    ship_routes = context['ship_routes']

    # Extract all BUY actions from all routes
    all_buy_pairs = []
    for ship_symbol, route in ship_routes.items():
        for segment in route.segments:
            for action in segment.actions_at_destination:
                if action.action == 'BUY':
                    buy_pair = (action.good, action.waypoint)
                    all_buy_pairs.append((ship_symbol, buy_pair))

    # Check for duplicates
    seen = set()
    for ship_symbol, buy_pair in all_buy_pairs:
        assert buy_pair not in seen, \
            f"Conflict detected: {buy_pair} is used by multiple ships"
        seen.add(buy_pair)


@then(parsers.parse('ship "{ship}" should buy "{good}" at "{waypoint}"'))
def check_ship_buys_good_at_waypoint(context, ship, good, waypoint):
    """Verify ship buys specific good at specific waypoint"""
    ship_routes = context['ship_routes']
    assert ship in ship_routes, f"Ship {ship} has no route"

    route = ship_routes[ship]
    found = False

    for segment in route.segments:
        for action in segment.actions_at_destination:
            if action.action == 'BUY' and action.good == good and action.waypoint == waypoint:
                found = True
                break
        if found:
            break

    assert found, f"Ship {ship} does not buy {good} at {waypoint}"


@then('total fleet profit should be greater than 0')
def check_total_fleet_profit_positive(context):
    """Verify total fleet profit is positive"""
    result = context['fleet_result']
    assert result is not None, "Fleet result is None"
    assert result['total_fleet_profit'] > 0, \
        f"Total fleet profit should be positive, got {result['total_fleet_profit']}"


@then(parsers.parse('the result should indicate {assigned:d} out of {total:d} ships have routes'))
def check_ship_assignment_count(context, assigned, total):
    """Verify correct number of ships have routes"""
    ship_routes = context['ship_routes']
    assert len(ship_routes) == assigned, \
        f"Expected {assigned} ships with routes, got {len(ship_routes)}"


@then('reserved resource pairs should equal the count of unique BUY actions')
def check_reserved_pairs_count(context):
    """Verify reserved pairs count matches unique BUY actions"""
    ship_routes = context['ship_routes']
    reserved_pairs = context['reserved_pairs']

    # Count unique BUY actions
    unique_buys = set()
    for route in ship_routes.values():
        for segment in route.segments:
            for action in segment.actions_at_destination:
                if action.action == 'BUY':
                    unique_buys.add((action.good, action.waypoint))

    assert len(reserved_pairs) == len(unique_buys), \
        f"Reserved pairs ({len(reserved_pairs)}) should equal unique BUYs ({len(unique_buys)})"


@then(parsers.parse('ship "{ship}" route should account for {units:d} units of existing cargo'))
def check_route_accounts_for_existing_cargo(context, ship, units):
    """Verify route considers existing cargo capacity"""
    ship_routes = context['ship_routes']

    # Get ship data to verify cargo was passed
    ship_data = next((s for s in context['ships'] if s['symbol'] == ship), None)
    assert ship_data is not None, f"Ship {ship} not found"

    existing_cargo = sum(item['units'] for item in ship_data['cargo']['inventory'])
    assert existing_cargo == units, f"Expected {units} units existing cargo, got {existing_cargo}"

    # Route should exist and be profitable despite reduced capacity
    assert ship in ship_routes, f"Ship {ship} should have a route"
    assert ship_routes[ship].total_profit > 0


@then('both routes should be profitable considering residual cargo')
def check_both_routes_profitable_with_cargo(context):
    """Verify both routes are profitable despite residual cargo"""
    ship_routes = context['ship_routes']
    assert len(ship_routes) >= 2, f"Expected at least 2 routes, got {len(ship_routes)}"

    for ship_symbol, route in ship_routes.items():
        assert route.total_profit > 0, \
            f"Route for {ship_symbol} should be profitable, got {route.total_profit}"


@then(parsers.parse('ship "{ship}" should buy units appropriate for capacity {capacity:d}'))
def check_ship_buys_appropriate_units(context, ship, capacity):
    """Verify ship buys appropriate units for its capacity"""
    ship_routes = context['ship_routes']
    assert ship in ship_routes, f"Ship {ship} has no route"

    route = ship_routes[ship]

    # Sum all BUY units
    total_buy_units = 0
    for segment in route.segments:
        for action in segment.actions_at_destination:
            if action.action == 'BUY':
                total_buy_units += action.units

    # Should not exceed capacity
    assert total_buy_units <= capacity, \
        f"Ship {ship} buys {total_buy_units} units, exceeds capacity {capacity}"

    # Should use reasonable portion of capacity (>10%)
    assert total_buy_units > capacity * 0.1, \
        f"Ship {ship} only buys {total_buy_units} units, inefficient use of capacity {capacity}"


@then(parsers.parse('ship "{ship}" should show no profitable routes in logs'))
def check_no_profitable_routes_logged(context, ship):
    """Verify logger shows no profitable routes for ship"""
    logger = context['logger']

    # Check that warning was logged about no profitable route
    warning_calls = [call for call in logger.warning.call_args_list]
    found_warning = any(ship in str(call) for call in warning_calls)

    assert found_warning, f"Expected warning log for {ship} having no profitable route"


@then('the result should contain "ship_routes" dictionary')
def check_result_has_ship_routes(context):
    """Verify result has ship_routes key"""
    result = context['fleet_result']
    assert result is not None, "Result is None"
    assert 'ship_routes' in result, "Result missing 'ship_routes' key"
    assert isinstance(result['ship_routes'], dict), "ship_routes should be a dict"


@then('the result should contain "total_fleet_profit"')
def check_result_has_total_fleet_profit(context):
    """Verify result has total_fleet_profit key"""
    result = context['fleet_result']
    assert result is not None, "Result is None"
    assert 'total_fleet_profit' in result, "Result missing 'total_fleet_profit' key"


@then('the result should contain "reserved_pairs" set')
def check_result_has_reserved_pairs(context):
    """Verify result has reserved_pairs key"""
    result = context['fleet_result']
    assert result is not None, "Result is None"
    assert 'reserved_pairs' in result, "Result missing 'reserved_pairs' key"
    assert isinstance(result['reserved_pairs'], set), "reserved_pairs should be a set"


@then(parsers.parse('the result should show "conflicts" equals {count:d}'))
def check_conflicts_count(context, count):
    """Verify conflicts count in result"""
    result = context['fleet_result']
    assert result is not None, "Result is None"
    assert 'conflicts' in result, "Result missing 'conflicts' key"
    assert result['conflicts'] == count, \
        f"Expected {count} conflicts, got {result['conflicts']}"


@then('"total_fleet_profit" should equal sum of individual route profits')
def check_total_profit_matches_sum(context):
    """Verify total_fleet_profit equals sum of individual routes"""
    result = context['fleet_result']
    ship_routes = result['ship_routes']

    expected_total = sum(route.total_profit for route in ship_routes.values())
    actual_total = result['total_fleet_profit']

    assert actual_total == expected_total, \
        f"Total fleet profit {actual_total} should equal sum {expected_total}"


@then('the result should be None')
def check_result_is_none(context):
    """Verify result is None"""
    result = context['fleet_result']
    assert result is None, f"Expected None result, got {result}"


@then('the error log should indicate no profitable routes found')
def check_error_log_no_profitable_routes(context):
    """Verify error log shows no profitable routes"""
    logger = context['logger']

    # Check that error was logged
    error_calls = [call for call in logger.error.call_args_list]
    found_error = any('no profitable' in str(call).lower() for call in error_calls)

    assert found_error, "Expected error log indicating no profitable routes"


@then(parsers.parse('reserved pairs should include all BUY actions from ship "{ship}"'))
def check_reserved_pairs_include_ship_buys(context, ship):
    """Verify reserved pairs include all BUY actions from specific ship"""
    ship_routes = context['ship_routes']
    reserved_pairs = context['reserved_pairs']

    if ship not in ship_routes:
        # Ship has no route, skip check
        return

    route = ship_routes[ship]

    # Extract all BUY actions from this ship's route
    ship_buy_pairs = set()
    for segment in route.segments:
        for action in segment.actions_at_destination:
            if action.action == 'BUY':
                ship_buy_pairs.add((action.good, action.waypoint))

    # All should be in reserved pairs
    for pair in ship_buy_pairs:
        assert pair in reserved_pairs, \
            f"BUY pair {pair} from {ship} not in reserved pairs"


@then('no reserved pair should appear in multiple ship routes')
def check_no_duplicate_reserved_pairs(context):
    """Verify no reserved pair is used by multiple ships"""
    ship_routes = context['ship_routes']

    # Track which ship uses which pairs
    pair_to_ship = {}

    for ship_symbol, route in ship_routes.items():
        for segment in route.segments:
            for action in segment.actions_at_destination:
                if action.action == 'BUY':
                    pair = (action.good, action.waypoint)

                    if pair in pair_to_ship:
                        pytest.fail(
                            f"Pair {pair} used by both {pair_to_ship[pair]} and {ship_symbol}"
                        )

                    pair_to_ship[pair] = ship_symbol


@then('reserved pairs should not be empty')
def check_reserved_pairs_not_empty(context):
    """Verify reserved pairs is not empty"""
    reserved_pairs = context['reserved_pairs']
    assert len(reserved_pairs) > 0, "Reserved pairs should not be empty"


@then(parsers.parse('total fleet profit should equal ship "{ship}" route profit'))
def check_total_equals_single_ship_profit(context, ship):
    """Verify total fleet profit equals single ship's profit"""
    result = context['fleet_result']
    ship_routes = context['ship_routes']

    assert ship in ship_routes, f"Ship {ship} has no route"

    single_ship_profit = ship_routes[ship].total_profit
    total_fleet_profit = result['total_fleet_profit']

    assert total_fleet_profit == single_ship_profit, \
        f"Total fleet profit {total_fleet_profit} should equal {ship} profit {single_ship_profit}"
