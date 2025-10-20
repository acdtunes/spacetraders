from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, patch
from contextlib import contextmanager
from dataclasses import dataclass, field
import json
import io
import sys

from spacetraders_bot.operations.waypoint_query import waypoint_query_operation

scenarios('../../../bdd/features/operations/waypoint_query.feature')


@dataclass
class MockWaypoint:
    """Mock waypoint data."""
    waypoint_symbol: str
    type: str = "PLANET"
    x: int = 0
    y: int = 0
    traits: list = field(default_factory=list)
    has_fuel: bool = False
    orbitals: list = field(default_factory=list)
    system_symbol: str = "X1-TEST"


class MockCursor:
    """Mock database cursor."""
    def __init__(self, waypoints):
        self.waypoints = waypoints
        self.results = []

    def execute(self, query, params):
        """Execute mock query with proper filtering."""
        results = []
        system = params[0] if params else None

        for wp in self.waypoints:
            if system and wp.system_symbol != system:
                continue

            # Check type filter
            if "type = ?" in query and len(params) > 1:
                if wp.type != params[1]:
                    continue

            # Check has_fuel filter
            if "has_fuel = 1" in query and not wp.has_fuel:
                continue

            # Check trait filter (positive)
            if "traits LIKE ?" in query:
                # Find first param that starts with %"
                trait_params = [p for p in params[1:] if isinstance(p, str) and p.startswith('%"')]
                if trait_params:
                    trait = trait_params[0].strip('%"')
                    if trait not in wp.traits:
                        continue

            # Check exclude filters (negative)
            if "traits NOT LIKE ?" in query:
                # Find params that start with %"
                exclude_params = [p for p in params[1:] if isinstance(p, str) and p.startswith('%"')]
                excluded = False
                for exclude_param in exclude_params:
                    exclude_trait = exclude_param.strip('%"')
                    if exclude_trait in wp.traits:
                        excluded = True
                        break
                if excluded:
                    continue

            results.append(wp)

        self.results = results
        return self

    def fetchall(self):
        """Return mock results as dict-like objects."""
        return [{
            'waypoint_symbol': wp.waypoint_symbol,
            'type': wp.type,
            'x': wp.x,
            'y': wp.y,
            'traits': json.dumps(wp.traits),
            'has_fuel': 1 if wp.has_fuel else 0,
            'orbitals': json.dumps(wp.orbitals)
        } for wp in self.results]


class MockConnection:
    """Mock database connection."""
    def __init__(self, waypoints):
        self.waypoints = waypoints

    def cursor(self):
        """Return mock cursor."""
        return MockCursor(self.waypoints)


class MockDatabase:
    """Mock database."""
    def __init__(self, waypoints):
        self.waypoints = waypoints

    @contextmanager
    def connection(self):
        """Return mock connection context manager."""
        conn = MockConnection(self.waypoints)
        try:
            yield conn
        finally:
            pass


@given('a waypoint query system with database', target_fixture='waypoint_query_ctx')
def given_waypoint_query_system():
    """Create waypoint query system."""
    return {
        'waypoints': [],
        'result_code': None,
        'output': None
    }


@given(parsers.re(r'system "(?P<system>[^"]+)" has (?P<count>\d+) waypoints'))
def given_system_waypoints(waypoint_query_ctx, system, count):
    """Create waypoints in system."""
    count = int(count)
    for i in range(count):
        wp = MockWaypoint(
            waypoint_symbol=f"{system}-{chr(65+i)}{i+1}",
            system_symbol=system
        )
        waypoint_query_ctx['waypoints'].append(wp)


@given(parsers.re(r'system "(?P<system>[^"]+)" has waypoint "(?P<symbol>[^"]+)" of type "(?P<wp_type>[^"]+)" with trait "(?P<trait>[^"]+)"'))
def given_waypoint_type_and_trait(waypoint_query_ctx, system, symbol, wp_type, trait):
    """Create waypoint with type and trait."""
    wp = MockWaypoint(
        waypoint_symbol=symbol,
        system_symbol=system,
        type=wp_type,
        traits=[trait]
    )
    waypoint_query_ctx['waypoints'].append(wp)


@given(parsers.re(r'system "(?P<system>[^"]+)" has waypoint "(?P<symbol>[^"]+)" of type "(?P<wp_type>[^"]+)"$'))
def given_waypoint_with_type(waypoint_query_ctx, system, symbol, wp_type):
    """Create waypoint with type."""
    wp = MockWaypoint(
        waypoint_symbol=symbol,
        system_symbol=system,
        type=wp_type
    )
    waypoint_query_ctx['waypoints'].append(wp)


@given(parsers.re(r'system "(?P<system>[^"]+)" has waypoint "(?P<symbol>[^"]+)" with trait "(?P<trait>[^"]+)"'))
def given_waypoint_with_trait(waypoint_query_ctx, system, symbol, trait):
    """Create waypoint with trait."""
    wp = MockWaypoint(
        waypoint_symbol=symbol,
        system_symbol=system,
        traits=[trait]
    )
    waypoint_query_ctx['waypoints'].append(wp)


@given(parsers.re(r'system "(?P<system>[^"]+)" has waypoint "(?P<symbol>[^"]+)" with fuel'))
def given_waypoint_with_fuel(waypoint_query_ctx, system, symbol):
    """Create waypoint with fuel."""
    wp = MockWaypoint(
        waypoint_symbol=symbol,
        system_symbol=system,
        has_fuel=True
    )
    waypoint_query_ctx['waypoints'].append(wp)


@given(parsers.re(r'system "(?P<system>[^"]+)" has waypoint "(?P<symbol>[^"]+)" without fuel'))
def given_waypoint_without_fuel(waypoint_query_ctx, system, symbol):
    """Create waypoint without fuel."""
    wp = MockWaypoint(
        waypoint_symbol=symbol,
        system_symbol=system,
        has_fuel=False
    )
    waypoint_query_ctx['waypoints'].append(wp)


@given(parsers.re(r'system "(?P<system>[^"]+)" has (?P<count>\d+) waypoints without "(?P<trait>[^"]+)" trait'))
def given_waypoints_without_trait(waypoint_query_ctx, system, count, trait):
    """Create waypoints without specific trait."""
    count = int(count)
    for i in range(count):
        wp = MockWaypoint(
            waypoint_symbol=f"{system}-{chr(65+i)}{i+1}",
            system_symbol=system,
            traits=["OTHER_TRAIT"]
        )
        waypoint_query_ctx['waypoints'].append(wp)


@given(parsers.re(r'system "(?P<system>[^"]+)" has waypoint "(?P<symbol>[^"]+)" at coordinates \((?P<x>-?\d+), (?P<y>-?\d+)\)'))
def given_waypoint_coordinates(waypoint_query_ctx, system, symbol, x, y):
    """Create waypoint with coordinates."""
    wp = MockWaypoint(
        waypoint_symbol=symbol,
        system_symbol=system,
        x=int(x),
        y=int(y)
    )
    waypoint_query_ctx['waypoints'].append(wp)


@given(parsers.re(r'system "(?P<system>[^"]+)" has waypoint "(?P<symbol>[^"]+)" with orbitals "(?P<orbitals>[^"]+)"'))
def given_waypoint_orbitals(waypoint_query_ctx, system, symbol, orbitals):
    """Create waypoint with orbitals."""
    orbitals_list = [o.strip() for o in orbitals.split(',')]
    wp = MockWaypoint(
        waypoint_symbol=symbol,
        system_symbol=system,
        orbitals=orbitals_list
    )
    waypoint_query_ctx['waypoints'].append(wp)


def _run_query(waypoint_query_ctx, system, wp_type=None, trait=None, exclude=None, has_fuel=False):
    """Helper to run waypoint query."""
    args = Mock()
    args.player_id = 1
    args.system = system
    args.waypoint_type = wp_type
    args.trait = trait
    args.exclude = exclude
    args.has_fuel = has_fuel
    args.log_level = 'ERROR'

    # Capture stdout
    captured_output = io.StringIO()
    sys.stdout = captured_output

    # Mock database and API
    db = MockDatabase(waypoint_query_ctx['waypoints'])

    with patch('spacetraders_bot.operations.waypoint_query.get_database', return_value=db):
        with patch('spacetraders_bot.operations.waypoint_query.get_api_client', return_value=Mock()):
            result = waypoint_query_operation(args)

    # Restore stdout
    sys.stdout = sys.__stdout__

    waypoint_query_ctx['output'] = captured_output.getvalue()
    waypoint_query_ctx['result_code'] = result


@when(parsers.re(r'I query waypoints for system "(?P<system>[^"]+)" with type "(?P<wp_type>[^"]+)"$'))
def when_query_with_type(waypoint_query_ctx, system, wp_type):
    """Query waypoints with type filter."""
    _run_query(waypoint_query_ctx, system, wp_type=wp_type)


@when(parsers.re(r'I query waypoints for system "(?P<system>[^"]+)" with trait "(?P<trait>[^"]+)"'))
def when_query_with_trait(waypoint_query_ctx, system, trait):
    """Query waypoints with trait filter."""
    _run_query(waypoint_query_ctx, system, trait=trait)


@when(parsers.re(r'I query waypoints for system "(?P<system>[^"]+)" excluding traits? "(?P<exclude>[^"]+)"'))
def when_query_excluding_trait(waypoint_query_ctx, system, exclude):
    """Query waypoints excluding trait."""
    _run_query(waypoint_query_ctx, system, exclude=exclude)
    waypoint_query_ctx['excluded_traits'] = [t.strip() for t in exclude.split(',')]


@when(parsers.re(r'I query waypoints for system "(?P<system>[^"]+)" with has_fuel filter'))
def when_query_with_fuel(waypoint_query_ctx, system):
    """Query waypoints with fuel filter."""
    _run_query(waypoint_query_ctx, system, has_fuel=True)


@when(parsers.re(r'I query waypoints for system "(?P<system>[^"]+)" with type "(?P<wp_type>[^"]+)" and trait "(?P<trait>[^"]+)"'))
def when_query_type_and_trait(waypoint_query_ctx, system, wp_type, trait):
    """Query waypoints with type and trait filter."""
    _run_query(waypoint_query_ctx, system, wp_type=wp_type, trait=trait)


@when(parsers.re(r'I query waypoints for system "(?P<system>[^"]+)"$'))
def when_query_waypoints(waypoint_query_ctx, system):
    """Query waypoints for system."""
    _run_query(waypoint_query_ctx, system)


def _get_waypoint_lines(output):
    """Extract waypoint result lines from output."""
    lines = output.split('\n')
    # Filter for lines that contain waypoint symbols (X1-*) but exclude headers/footers
    waypoint_lines = []
    for line in lines:
        # Must contain X1- and not be a header line or debug line
        if 'X1-' in line and '=' not in line and 'WAYPOINT QUERY' not in line and 'Filter:' not in line and 'Total:' not in line and '[DEBUG]' not in line:
            waypoint_lines.append(line.strip())
    return [line for line in waypoint_lines if line]  # Remove empty lines


@then(parsers.parse('{count:d} waypoints should be returned'))
@then(parsers.parse('{count:d} waypoint should be returned'))
def then_waypoint_count(waypoint_query_ctx, count):
    """Verify waypoint count."""
    lines = _get_waypoint_lines(waypoint_query_ctx['output'])
    assert len(lines) == count, f"Expected {count} waypoints, got {len(lines)}: {lines}"


@then(parsers.parse('all waypoints should be in system "{system}"'))
def then_all_in_system(waypoint_query_ctx, system):
    """Verify all waypoints in system."""
    lines = _get_waypoint_lines(waypoint_query_ctx['output'])
    for line in lines:
        assert system in line


@then(parsers.parse('all returned waypoints should have type "{wp_type}"'))
def then_all_have_type(waypoint_query_ctx, wp_type):
    """Verify all waypoints have type."""
    lines = _get_waypoint_lines(waypoint_query_ctx['output'])
    for line in lines:
        assert wp_type in line


@then(parsers.parse('all returned waypoints should have trait "{trait}"'))
def then_all_have_trait(waypoint_query_ctx, trait):
    """Verify all waypoints have trait."""
    lines = _get_waypoint_lines(waypoint_query_ctx['output'])
    for line in lines:
        assert trait in line


@then(parsers.parse('no returned waypoints should have trait "{trait}"'))
def then_none_have_trait(waypoint_query_ctx, trait):
    """Verify no waypoints have trait."""
    lines = _get_waypoint_lines(waypoint_query_ctx['output'])
    for line in lines:
        assert trait not in line


@then('no returned waypoints should have excluded traits')
def then_none_have_excluded(waypoint_query_ctx):
    """Verify no waypoints have excluded traits."""
    lines = _get_waypoint_lines(waypoint_query_ctx['output'])
    excluded = waypoint_query_ctx.get('excluded_traits', [])
    for line in lines:
        for trait in excluded:
            assert trait not in line


@then(parsers.parse('returned waypoint should be "{waypoint}"'))
def then_returned_waypoint(waypoint_query_ctx, waypoint):
    """Verify specific waypoint returned."""
    lines = _get_waypoint_lines(waypoint_query_ctx['output'])
    assert len(lines) == 1
    assert waypoint in lines[0]


@then('query should indicate no matches found')
def then_no_matches(waypoint_query_ctx):
    """Verify no matches message."""
    assert 'No waypoints found' in waypoint_query_ctx['output']
    assert waypoint_query_ctx['result_code'] == 1


@then(parsers.parse('waypoint "{waypoint}" should show coordinates ({x:d}, {y:d})'))
def then_waypoint_coordinates(waypoint_query_ctx, waypoint, x, y):
    """Verify waypoint coordinates."""
    coord_str = f"({x}, {y})"
    lines = _get_waypoint_lines(waypoint_query_ctx['output'])
    found = False
    for line in lines:
        if waypoint in line and coord_str in line:
            found = True
            break
    assert found, f"Waypoint {waypoint} with coordinates {coord_str} not found"


@then(parsers.parse('waypoint "{waypoint}" should list orbitals'))
def then_waypoint_orbitals(waypoint_query_ctx, waypoint):
    """Verify waypoint orbitals are listed."""
    assert "Orbitals:" in waypoint_query_ctx['output']
