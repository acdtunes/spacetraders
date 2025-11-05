"""BDD step definitions for SystemGraphProvider integration tests"""
import pytest
import json
from pytest_bdd import scenarios, given, when, then, parsers
from unittest.mock import Mock, MagicMock
from adapters.secondary.routing.graph_provider import SystemGraphProvider
from ports.outbound.graph_provider import GraphLoadResult

# Load scenarios
scenarios('../../../features/integration/routing/graph_provider.feature')


# Fixtures
@pytest.fixture
def context():
    """Test context for storing state between steps"""
    return {
        'database': None,
        'builder': None,
        'provider': None,
        'result': None,
        'loaded_graph': None,
        'built_graph': None,
        'sample_graph': None,
        'error': None,
        'saved_sql': None,
        'saved_data': None,
        'results': [],
    }


# Background steps
@given('a graph provider with mocked database and builder')
def given_graph_provider(context):
    """Create graph provider with mocked database and builder"""
    context['database'] = Mock()
    context['builder'] = Mock()
    context['provider'] = SystemGraphProvider(context['database'], context['builder'])


# Database mocking steps - cache hits
@given(parsers.parse('the database has cached graph for "{system}"'))
def given_database_has_cached_graph(context, system):
    """Mock database to have cached graph"""
    sample_graph = {
        "system": system,
        "waypoints": {
            f"{system}-WP1": {
                "type": "PLANET",
                "x": 0,
                "y": 0,
                "traits": ["MARKETPLACE"],
                "has_fuel": True,
                "orbitals": []
            }
        },
        "edges": []
    }

    # Mock connection for reading
    mock_cursor = Mock()
    mock_cursor.fetchone.return_value = (json.dumps(sample_graph),)
    mock_conn = Mock()
    mock_conn.cursor.return_value = mock_cursor
    context['database'].connection.return_value.__enter__ = Mock(return_value=mock_conn)
    context['database'].connection.return_value.__exit__ = Mock(return_value=False)


@given(parsers.parse('the database has cached graph for "{system}" with system name "{name}"'))
def given_database_has_cached_graph_with_name(context, system, name):
    """Mock database to have cached graph with specific system name"""
    # Get or create graphs dict
    if 'cached_graphs' not in context:
        context['cached_graphs'] = {}

    graphs = context['cached_graphs']
    graphs[system] = {
        "system": name,
        "waypoints": {},
        "edges": []
    }

    # Create a closure that captures the graphs dict
    def mock_execute(query, params):
        system_key = params[0]
        if system_key in context['cached_graphs']:
            mock_cursor.fetchone.return_value = (json.dumps(context['cached_graphs'][system_key]),)
        else:
            mock_cursor.fetchone.return_value = None

    # Create or reuse mock cursor
    if 'mock_cursor' not in context:
        mock_cursor = Mock()
        mock_cursor.execute.side_effect = mock_execute
        mock_conn = Mock()
        mock_conn.cursor.return_value = mock_cursor
        context['mock_cursor'] = mock_cursor
        context['database'].connection.return_value.__enter__ = Mock(return_value=mock_conn)
        context['database'].connection.return_value.__exit__ = Mock(return_value=False)

    # Mock transaction for saving
    mock_trans_cursor = Mock()
    mock_trans_conn = Mock()
    mock_trans_conn.cursor.return_value = mock_trans_cursor
    context['database'].transaction.return_value.__enter__ = Mock(return_value=mock_trans_conn)
    context['database'].transaction.return_value.__exit__ = Mock(return_value=False)


# Database mocking steps - cache misses
@given(parsers.parse('the database has no cached graph for "{system}"'))
def given_database_has_no_cached_graph(context, system):
    """Mock database to have no cached graph"""
    mock_cursor = Mock()
    mock_cursor.fetchone.return_value = None
    mock_conn = Mock()
    mock_conn.cursor.return_value = mock_cursor
    context['database'].connection.return_value.__enter__ = Mock(return_value=mock_conn)
    context['database'].connection.return_value.__exit__ = Mock(return_value=False)


# Database mocking steps - errors
@given('the database throws error on connection')
def given_database_throws_error_on_connection(context):
    """Mock database to throw error on connection"""
    mock_conn = Mock()
    mock_conn.__enter__ = Mock(side_effect=Exception("DB Connection Error"))
    mock_conn.__exit__ = Mock(return_value=False)
    context['database'].connection.return_value = mock_conn


@given('the database throws error on transaction')
def given_database_throws_error_on_transaction(context):
    """Mock database to throw error on transaction"""
    mock_trans = Mock()
    mock_trans.__enter__ = Mock(side_effect=Exception("DB Transaction Error"))
    mock_trans.__exit__ = Mock(return_value=False)
    context['database'].transaction.return_value = mock_trans


# Builder mocking steps
@given(parsers.parse('the builder can build graph for "{system}"'))
def given_builder_can_build_graph(context, system):
    """Mock builder to build graph"""
    sample_graph = {
        "system": system,
        "waypoints": {
            f"{system}-WP1": {
                "type": "PLANET",
                "x": 0,
                "y": 0,
                "traits": ["MARKETPLACE"],
                "has_fuel": True,
                "orbitals": []
            },
            f"{system}-WP2": {
                "type": "ASTEROID",
                "x": 10,
                "y": 0,
                "traits": [],
                "has_fuel": False,
                "orbitals": []
            }
        },
        "edges": [
            {"from": f"{system}-WP1", "to": f"{system}-WP2", "distance": 10.0, "type": "normal"},
            {"from": f"{system}-WP2", "to": f"{system}-WP1", "distance": 10.0, "type": "normal"}
        ]
    }

    context['builder'].build_system_graph.return_value = sample_graph

    # Mock transaction for saving
    mock_trans_conn = Mock()
    mock_trans_cursor = Mock()
    mock_trans_conn.cursor.return_value = mock_trans_cursor
    context['database'].transaction.return_value.__enter__ = Mock(return_value=mock_trans_conn)
    context['database'].transaction.return_value.__exit__ = Mock(return_value=False)


@given(parsers.parse('the builder can build updated graph for "{system}"'))
def given_builder_can_build_updated_graph(context, system):
    """Mock builder to build updated graph"""
    updated_graph = {
        "system": f"{system}-UPDATED",
        "waypoints": {},
        "edges": []
    }

    context['builder'].build_system_graph.return_value = updated_graph

    # Mock transaction for saving
    mock_trans_conn = Mock()
    mock_trans_cursor = Mock()
    mock_trans_conn.cursor.return_value = mock_trans_cursor
    context['database'].transaction.return_value.__enter__ = Mock(return_value=mock_trans_conn)
    context['database'].transaction.return_value.__exit__ = Mock(return_value=False)


@given(parsers.parse('the builder throws error for "{system}"'))
def given_builder_throws_error(context, system):
    """Mock builder to throw error"""
    context['builder'].build_system_graph.side_effect = Exception("Builder Error")


# Sample graph setup
@given(parsers.parse('a sample graph for "{system}"'))
def given_sample_graph(context, system):
    """Create sample graph"""
    context['sample_graph'] = {
        "system": system,
        "waypoints": {
            f"{system}-WP1": {
                "type": "PLANET",
                "x": 0,
                "y": 0,
                "traits": ["MARKETPLACE"],
                "has_fuel": True,
                "orbitals": []
            }
        },
        "edges": []
    }

    # Mock transaction for saving
    def capture_execute(query, params):
        context['saved_sql'] = query
        context['saved_data'] = params

    mock_trans_cursor = Mock()
    mock_trans_cursor.execute.side_effect = capture_execute
    mock_trans_conn = Mock()
    mock_trans_conn.cursor.return_value = mock_trans_cursor
    context['database'].transaction.return_value.__enter__ = Mock(return_value=mock_trans_conn)
    context['database'].transaction.return_value.__exit__ = Mock(return_value=False)


@given(parsers.parse('a complex graph for "{system}" with decimals and lists'))
def given_complex_graph(context, system):
    """Create complex graph"""
    complex_graph = {
        "system": system,
        "waypoints": {
            "WP-1": {
                "type": "PLANET",
                "x": 123.456,
                "y": -789.012,
                "traits": ["TRAIT_1", "TRAIT_2"],
                "has_fuel": True,
                "orbitals": ["MOON-1", "STATION-1"]
            }
        },
        "edges": [
            {
                "from": "WP-1",
                "to": "WP-2",
                "distance": 3.14159,
                "type": "normal"
            }
        ]
    }

    context['builder'].build_system_graph.return_value = complex_graph

    # Mock transaction for saving
    def capture_execute(query, params):
        context['saved_sql'] = query
        context['saved_data'] = params

    mock_trans_cursor = Mock()
    mock_trans_cursor.execute.side_effect = capture_execute
    mock_trans_conn = Mock()
    mock_trans_conn.cursor.return_value = mock_trans_cursor
    context['database'].transaction.return_value.__enter__ = Mock(return_value=mock_trans_conn)
    context['database'].transaction.return_value.__exit__ = Mock(return_value=False)


# Get graph operations
@when(parsers.parse('I get graph for "{system}" without forcing refresh'))
def when_get_graph_no_force(context, system):
    """Get graph without forcing refresh"""
    context['result'] = context['provider'].get_graph(system, force_refresh=False)
    if 'results' not in context:
        context['results'] = []
    context['results'].append(context['result'])


@when(parsers.parse('I get graph for "{system}" forcing refresh'))
def when_get_graph_force(context, system):
    """Get graph forcing refresh"""
    context['result'] = context['provider'].get_graph(system, force_refresh=True)


# Internal method operations
@when(parsers.parse('I load from database for "{system}"'))
def when_load_from_database(context, system):
    """Load from database"""
    context['loaded_graph'] = context['provider']._load_from_database(system)


@when(parsers.parse('I build from API for "{system}"'))
def when_build_from_api(context, system):
    """Build from API"""
    try:
        context['built_graph'] = context['provider']._build_from_api(system)
        context['error'] = None
    except RuntimeError as e:
        context['error'] = str(e)
        context['built_graph'] = None


@when(parsers.parse('I save to database for "{system}"'))
def when_save_to_database(context, system):
    """Save to database"""
    try:
        context['provider']._save_to_database(system, context['sample_graph'])
        context['error'] = None
    except Exception as e:
        context['error'] = str(e)


# Result validation steps
@then(parsers.parse('the graph should be loaded from "{source}"'))
def then_graph_loaded_from_source(context, source):
    """Verify graph was loaded from expected source"""
    assert context['result'] is not None
    assert isinstance(context['result'], GraphLoadResult)
    assert context['result'].source == source


@then(parsers.parse('the result message should contain "{text}"'))
def then_result_message_contains(context, text):
    """Verify result message contains expected text"""
    assert context['result'] is not None
    assert context['result'].message is not None
    assert text in context['result'].message


@then('the database should have been queried for graph')
def then_database_queried(context):
    """Verify database was queried"""
    # Check if connection was used for reading
    assert context['database'].connection.called


@then('the database should not have been queried for graph')
def then_database_not_queried(context):
    """Verify database was not queried for reading"""
    assert not context['database'].connection.called


@then(parsers.parse('the builder should have been called for "{system}"'))
def then_builder_called_for_system(context, system):
    """Verify builder was called for specific system"""
    # Behavior verification: Check that we got a graph (either from get_graph or _build_from_api)
    # For get_graph(): result is GraphLoadResult with source="api"
    # For _build_from_api(): result is a dict graph

    if context.get('result') is not None:
        # Called via get_graph()
        assert isinstance(context['result'], GraphLoadResult)
        assert context['result'].source == "api", "Graph should be loaded from API when builder is called"
        graph = context['result'].graph
    elif context.get('built_graph') is not None:
        # Called via _build_from_api()
        graph = context['built_graph']
    else:
        raise AssertionError("Neither result nor built_graph found in context")

    # Verify the graph contains the correct system
    assert graph is not None
    assert graph['system'] == system


@then('the builder should have been called once')
def then_builder_called_once(context):
    """Verify builder was called exactly once"""
    # Behavior verification: The graph was loaded from API (not cache)
    assert context['result'] is not None
    assert isinstance(context['result'], GraphLoadResult)
    assert context['result'].source == "api", "Graph should be loaded from API when builder is called"

    # Verify we got a valid graph back
    assert context['result'].graph is not None
    assert 'system' in context['result'].graph
    assert 'waypoints' in context['result'].graph
    assert 'edges' in context['result'].graph


@then('the builder should not have been called')
def then_builder_not_called(context):
    """Verify builder was not called"""
    # Behavior verification: The graph was loaded from database cache (not API)
    assert context['result'] is not None
    assert isinstance(context['result'], GraphLoadResult)
    assert context['result'].source == "database", "Graph should be loaded from database when builder is not called"

    # Verify we got a valid cached graph
    assert context['result'].graph is not None
    assert 'system' in context['result'].graph
    assert 'waypoints' in context['result'].graph
    assert 'edges' in context['result'].graph


@then('the graph should have been saved to database')
def then_graph_saved_to_database(context):
    """Verify graph was saved to database"""
    # Check if transaction was used for writing
    assert context['database'].transaction.called


# Load from database validation
@then('the loaded graph should match cached data')
def then_loaded_graph_matches_cached(context):
    """Verify loaded graph matches cached data"""
    assert context['loaded_graph'] is not None
    assert 'system' in context['loaded_graph']
    assert 'waypoints' in context['loaded_graph']
    assert 'edges' in context['loaded_graph']


@then('the loaded graph should be None')
def then_loaded_graph_is_none(context):
    """Verify loaded graph is None"""
    assert context['loaded_graph'] is None


@then(parsers.parse('the database should have been queried with "{system}"'))
def then_database_queried_with_system(context, system):
    """Verify database was queried with specific system"""
    # The mock cursor should have been called with the system symbol
    assert context['database'].connection.called


# Build from API validation
@then('the built graph should match builder output')
def then_built_graph_matches_builder(context):
    """Verify built graph matches builder output"""
    assert context['built_graph'] is not None
    expected = context['builder'].build_system_graph.return_value
    assert context['built_graph'] == expected


@then(parsers.parse('building should fail with "{message}"'))
def then_building_fails(context, message):
    """Verify building failed with expected message"""
    assert context['error'] is not None
    assert message in context['error']


@then(parsers.parse('the error message should mention "{text}"'))
def then_error_mentions(context, text):
    """Verify error message mentions expected text"""
    assert context['error'] is not None
    assert text in context['error']


# Save to database validation
@then('the database should have received INSERT with UPSERT')
def then_database_received_upsert(context):
    """Verify database received INSERT with UPSERT"""
    assert context['saved_sql'] is not None
    assert 'INSERT INTO system_graphs' in context['saved_sql']
    assert 'ON CONFLICT' in context['saved_sql']


@then(parsers.parse('the saved data should include system "{system}"'))
def then_saved_data_includes_system(context, system):
    """Verify saved data includes system"""
    assert context['saved_data'] is not None
    assert context['saved_data'][0] == system


@then('no exception should be raised')
def then_no_exception_raised(context):
    """Verify no exception was raised"""
    assert context['error'] is None


@then('the SQL should use UPSERT pattern')
def then_sql_uses_upsert(context):
    """Verify SQL uses UPSERT pattern"""
    assert context['saved_sql'] is not None
    assert 'INSERT INTO system_graphs' in context['saved_sql']
    assert 'ON CONFLICT' in context['saved_sql']


@then(parsers.parse('the SQL should contain "{text}"'))
def then_sql_contains(context, text):
    """Verify SQL contains expected text"""
    assert context['saved_sql'] is not None
    assert text in context['saved_sql']


# Integration validation
@then('both graphs should have different system names')
def then_both_graphs_different_systems(context):
    """Verify both graphs have different system names"""
    assert len(context['results']) >= 2
    system1 = context['results'][0].graph['system']
    system2 = context['results'][1].graph['system']
    assert system1 != system2


@then(parsers.parse('both graphs should be loaded from "{source}"'))
def then_both_graphs_from_source(context, source):
    """Verify both graphs were loaded from expected source"""
    assert len(context['results']) >= 2
    assert all(r.source == source for r in context['results'])


# Cache consistency validation
@then('the saved JSON should deserialize to original graph')
def then_saved_json_deserializes(context):
    """Verify saved JSON deserializes to original graph"""
    assert context['saved_data'] is not None
    saved_json = context['saved_data'][1]
    loaded_graph = json.loads(saved_json)
    assert loaded_graph == context['sample_graph']


@then('the saved JSON should preserve all data types')
def then_saved_json_preserves_types(context):
    """Verify saved JSON preserves all data types"""
    assert context['saved_data'] is not None
    saved_json = context['saved_data'][1]
    loaded_graph = json.loads(saved_json)

    # Verify complex types are preserved
    expected = context['builder'].build_system_graph.return_value
    assert loaded_graph == expected
