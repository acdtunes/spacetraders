#!/usr/bin/env python3
"""
Step definitions for utility functions BDD tests
"""
import sys
from pathlib import Path
import pytest
from pytest_bdd import scenarios, given, when, then, parsers
from datetime import datetime, timedelta, UTC

sys.path.insert(0, str(Path(__file__).resolve().parents[4] / 'lib'))

from utils import (
    calculate_distance,
    calculate_arrival_wait_time,
    parse_waypoint_symbol,
    select_flight_mode,
    timestamp,
    format_credits,
    timestamp_iso,
    resource_to_deposit_type,
    calculate_profit
)

# Load all scenarios from the feature file
scenarios('../../features/helpers/utility_functions.feature')


@pytest.fixture
def context():
    """Shared test context"""
    return {
        'wp1': None,
        'wp2': None,
        'distance': None,
        'arrival_time': None,
        'wait_time': None,
        'waypoint_symbol': None,
        'system': None,
        'waypoint': None,
        'current_fuel': None,
        'fuel_capacity': None,
        'distance_units': None,
        'require_return': None,
        'flight_mode': None,
        'timestamp_result': None,
        'formatted_credits': None,
        'iso_timestamp': None,
        'resource_symbol': None,
        'deposit_types': None,
        'buy_price': None,
        'sell_price': None,
        'units': None,
        'profit_result': None
    }


@given(parsers.parse('two waypoints at ({x1:d}, {y1:d}) and ({x2:d}, {y2:d})'))
def create_two_waypoints(context, x1, y1, x2, y2):
    """Create two waypoints with coordinates"""
    context['wp1'] = {"x": x1, "y": y1}
    context['wp2'] = {"x": x2, "y": y2}


@given(parsers.parse('an arrival time {seconds:d} seconds in the future'))
def create_future_arrival_time(context, seconds):
    """Create arrival time in the future"""
    future = datetime.now(UTC) + timedelta(seconds=seconds)
    context['arrival_time'] = future.isoformat().replace('+00:00', 'Z')


@given(parsers.parse('an arrival time {seconds:d} seconds in the past'))
def create_past_arrival_time(context, seconds):
    """Create arrival time in the past"""
    past = datetime.now(UTC) - timedelta(seconds=seconds)
    context['arrival_time'] = past.isoformat().replace('+00:00', 'Z')


@given("an arrival time at current time")
def create_current_arrival_time(context):
    """Create arrival time at current time"""
    now = datetime.now(UTC)
    context['arrival_time'] = now.isoformat().replace('+00:00', 'Z')


@given(parsers.parse('a waypoint symbol "{symbol}"'))
def create_waypoint_symbol(context, symbol):
    """Store waypoint symbol"""
    context['waypoint_symbol'] = symbol


@given(parsers.parse('a ship with {current:d}/{capacity:d} fuel'))
def create_ship_fuel_state(context, current, capacity):
    """Store ship fuel state"""
    context['current_fuel'] = current
    context['fuel_capacity'] = capacity


@given(parsers.parse('a distance of {distance:d} units'))
def create_distance(context, distance):
    """Store distance"""
    context['distance_units'] = distance


@given("return trip is required")
def require_return_trip(context):
    """Set return trip requirement"""
    context['require_return'] = True


@given("return trip is not required")
def no_return_trip(context):
    """Set no return trip requirement"""
    context['require_return'] = False


@when("I calculate the distance")
def calculate_waypoint_distance(context):
    """Calculate distance between waypoints"""
    context['distance'] = calculate_distance(context['wp1'], context['wp2'])


@when("I calculate the wait time")
def calculate_wait_time(context):
    """Calculate arrival wait time"""
    context['wait_time'] = calculate_arrival_wait_time(context['arrival_time'])


@when("I parse the waypoint symbol")
def parse_symbol(context):
    """Parse waypoint symbol"""
    context['system'], context['waypoint'] = parse_waypoint_symbol(context['waypoint_symbol'])


@when("I select the flight mode")
def select_mode(context):
    """Select flight mode based on conditions"""
    context['flight_mode'] = select_flight_mode(
        current_fuel=context['current_fuel'],
        fuel_capacity=context['fuel_capacity'],
        distance=context['distance_units'],
        require_return=context['require_return']
    )


@when("I generate a timestamp")
def generate_timestamp(context):
    """Generate a timestamp"""
    context['timestamp_result'] = timestamp()


@then(parsers.parse('the result should be {expected:d}'))
def result_should_be_exact(context, expected):
    """Verify exact distance result"""
    assert context['distance'] == expected, f"Expected {expected}, got {context['distance']}"


@then(parsers.parse('the result should be approximately {expected:f}'))
def result_should_be_approximate(context, expected):
    """Verify approximate distance result"""
    assert abs(context['distance'] - expected) < 0.1, \
        f"Expected ~{expected}, got {context['distance']}"


@then(parsers.parse('the wait time should be approximately {expected:d} seconds'))
def wait_time_approximately(context, expected):
    """Verify wait time is approximately correct"""
    assert abs(context['wait_time'] - expected) <= 5, \
        f"Expected ~{expected} seconds, got {context['wait_time']}"


@then(parsers.parse('the wait time should be {expected:d} seconds'))
def wait_time_exact(context, expected):
    """Verify exact wait time"""
    assert context['wait_time'] == expected, \
        f"Expected {expected} seconds, got {context['wait_time']}"


@then(parsers.parse('the wait time should be less than {max_seconds:d} seconds'))
def wait_time_less_than(context, max_seconds):
    """Verify wait time is less than threshold"""
    assert context['wait_time'] < max_seconds, \
        f"Wait time should be < {max_seconds}, got {context['wait_time']}"


@then(parsers.parse('the system should be "{expected_system}"'))
def system_should_be(context, expected_system):
    """Verify parsed system"""
    assert context['system'] == expected_system, \
        f"Expected system {expected_system}, got {context['system']}"


@then(parsers.parse('the waypoint should be "{expected_waypoint}"'))
def waypoint_should_be(context, expected_waypoint):
    """Verify parsed waypoint"""
    assert context['waypoint'] == expected_waypoint, \
        f"Expected waypoint {expected_waypoint}, got {context['waypoint']}"


@then(parsers.parse('the mode should be "{expected_mode}"'))
def mode_should_be(context, expected_mode):
    """Verify selected flight mode"""
    assert context['flight_mode'] == expected_mode, \
        f"Expected mode {expected_mode}, got {context['flight_mode']}"


@then("the timestamp should contain a colon")
def timestamp_contains_colon(context):
    """Verify timestamp contains colon"""
    assert ":" in context['timestamp_result'], \
        f"Timestamp should contain colon: {context['timestamp_result']}"


@then("the timestamp should be a string")
def timestamp_is_string(context):
    """Verify timestamp is a string"""
    assert isinstance(context['timestamp_result'], str), \
        f"Timestamp should be string, got {type(context['timestamp_result'])}"


@then("the timestamp should contain time components")
def timestamp_contains_time_components(context):
    """Verify timestamp has time components"""
    assert ":" in context['timestamp_result'], \
        "Timestamp should contain time separator"
    assert len(context['timestamp_result']) > 0, \
        "Timestamp should not be empty"


# New step definitions for additional util functions

@when(parsers.parse('I format {amount:d} credits'))
def format_credits_amount(context, amount):
    """Format credits amount"""
    context['formatted_credits'] = format_credits(amount)


@then(parsers.parse('the formatted credits should be "{expected}"'))
def formatted_credits_should_be(context, expected):
    """Verify formatted credits"""
    assert context['formatted_credits'] == expected, \
        f"Expected '{expected}', got '{context['formatted_credits']}'"


@when("I generate an ISO timestamp")
def generate_iso_timestamp(context):
    """Generate ISO timestamp"""
    context['iso_timestamp'] = timestamp_iso()


@then(parsers.parse('the ISO timestamp should contain "{expected_char}"'))
def iso_timestamp_contains(context, expected_char):
    """Verify ISO timestamp contains character"""
    assert expected_char in context['iso_timestamp'], \
        f"ISO timestamp should contain '{expected_char}': {context['iso_timestamp']}"


@then("the ISO timestamp should be a string")
def iso_timestamp_is_string(context):
    """Verify ISO timestamp is a string"""
    assert isinstance(context['iso_timestamp'], str), \
        f"ISO timestamp should be string, got {type(context['iso_timestamp'])}"


@when(parsers.parse('I map resource "{resource}" to deposit type'))
def map_resource_to_deposit(context, resource):
    """Map resource to deposit type"""
    context['resource_symbol'] = resource
    context['deposit_types'] = resource_to_deposit_type(resource)


@then(parsers.parse('the deposit types should include "{expected_type}"'))
def deposit_types_include(context, expected_type):
    """Verify deposit types include expected type"""
    assert expected_type in context['deposit_types'], \
        f"Expected '{expected_type}' in {context['deposit_types']}"


@given(parsers.parse('a buy price of {price:d} credits per unit'))
def set_buy_price(context, price):
    """Set buy price"""
    context['buy_price'] = price


@given(parsers.parse('a sell price of {price:d} credits per unit'))
def set_sell_price(context, price):
    """Set sell price"""
    context['sell_price'] = price


@given(parsers.parse('{units:d} units to trade'))
def set_units(context, units):
    """Set units to trade"""
    context['units'] = units


@when("I calculate the profit")
def calculate_trade_profit(context):
    """Calculate profit"""
    context['profit_result'] = calculate_profit(
        buy_price=context['buy_price'],
        sell_price=context['sell_price'],
        units=context['units'],
        distance=context['distance_units']
    )


@then(parsers.parse('the gross profit should be {expected:d}'))
def gross_profit_should_be(context, expected):
    """Verify gross profit"""
    assert context['profit_result']['gross_profit'] == expected, \
        f"Expected gross profit {expected}, got {context['profit_result']['gross_profit']}"


@then(parsers.parse('the net profit should be greater than {threshold:d}'))
def net_profit_greater_than(context, threshold):
    """Verify net profit exceeds threshold"""
    assert context['profit_result']['net_profit'] > threshold, \
        f"Net profit should be > {threshold}, got {context['profit_result']['net_profit']}"


@then(parsers.parse('the ROI should be greater than {threshold:d}'))
def roi_greater_than(context, threshold):
    """Verify ROI exceeds threshold"""
    assert context['profit_result']['roi'] > threshold, \
        f"ROI should be > {threshold}%, got {context['profit_result']['roi']}%"


@then(parsers.parse('the net profit should be {expected:d}'))
def net_profit_should_be(context, expected):
    """Verify exact net profit"""
    assert context['profit_result']['net_profit'] == expected, \
        f"Expected net profit {expected}, got {context['profit_result']['net_profit']}"
