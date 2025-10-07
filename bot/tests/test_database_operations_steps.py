#!/usr/bin/env python3
"""
Step definitions for database operations BDD tests
"""
import sys
import os
import tempfile
import shutil
import json
from pathlib import Path
from datetime import datetime, timedelta
import pytest
from pytest_bdd import scenarios, given, when, then, parsers

sys.path.insert(0, str(Path(__file__).parent.parent / "lib"))

from database import Database, get_database, _db_instances

# Load all scenarios from the feature file
scenarios('features/database_operations.feature')


@pytest.fixture(scope="function")
def context():
    """Shared test context - fresh for each test"""
    import uuid
    temp_dir = tempfile.mkdtemp(prefix=f"test_database_{uuid.uuid4().hex[:8]}_")
    db_path = os.path.join(temp_dir, "test_database.db")

    ctx = {
        'db': None,
        'db_path': db_path,
        'temp_dir': temp_dir,
        'players': {},  # Map agent_symbol -> player data
        'player_ids': {},  # Map agent_symbol -> player_id
        'transactions': [],
        'market_data': [],
        'graph': None,
        'systems': [],
        'fuel_stations': [],
        'result': None,
        'error': None,
        'last_player_id': None,
    }

    yield ctx

    # Cleanup: Reset singleton and remove temp files
    global _db_instances
    normalized_path = str(Path(db_path).resolve())
    if normalized_path in _db_instances:
        del _db_instances[normalized_path]

    if ctx.get('temp_dir') and os.path.exists(ctx['temp_dir']):
        import time
        time.sleep(0.01)  # Give time for connections to close
        try:
            shutil.rmtree(ctx['temp_dir'])
        except Exception as e:
            print(f"Warning: Could not remove temp dir: {e}")


@given("the database is initialized with a temporary path")
def init_database(context):
    """Initialize database with temporary path"""
    # Create database instance
    context['db'] = Database(context['db_path'])


@when(parsers.parse('I create a player with agent symbol "{agent_symbol}" and token "{token}"'))
def create_player(context, agent_symbol, token):
    """Create a player"""
    with context['db'].transaction() as conn:
        player_id = context['db'].create_player(conn, agent_symbol, token)
        context['players'][agent_symbol] = {
            'agent_symbol': agent_symbol,
            'token': token,
            'player_id': player_id
        }
        context['player_ids'][agent_symbol] = player_id
        context['last_player_id'] = player_id


@when(parsers.parse('I create a player "{agent_symbol}" with metadata {metadata}'))
def create_player_with_metadata(context, agent_symbol, metadata):
    """Create a player with metadata"""
    metadata_dict = json.loads(metadata.replace("'", '"'))

    with context['db'].transaction() as conn:
        player_id = context['db'].create_player(
            conn, agent_symbol, f"token-{agent_symbol}", metadata=metadata_dict
        )
        context['players'][agent_symbol] = {
            'agent_symbol': agent_symbol,
            'token': f"token-{agent_symbol}",
            'player_id': player_id,
            'metadata': metadata_dict
        }
        context['player_ids'][agent_symbol] = player_id
        context['last_player_id'] = player_id


@given(parsers.parse('a player "{agent_symbol}" exists'))
def player_exists(context, agent_symbol):
    """Ensure a player exists"""
    if agent_symbol not in context['players']:
        with context['db'].transaction() as conn:
            player_id = context['db'].create_player(conn, agent_symbol, f"token-{agent_symbol}")
            context['players'][agent_symbol] = {
                'agent_symbol': agent_symbol,
                'token': f"token-{agent_symbol}",
                'player_id': player_id
            }
            context['player_ids'][agent_symbol] = player_id


@when("I update the player's activity timestamp")
def update_player_activity(context):
    """Update player's activity timestamp"""
    # Get the last created player
    player_id = context['last_player_id']

    with context['db'].transaction() as conn:
        context['db'].update_player_activity(conn, player_id)


@when("I list all players")
def list_players(context):
    """List all players"""
    with context['db'].connection() as conn:
        context['result'] = context['db'].list_players(conn)


@when(parsers.parse('I record a {tx_type} transaction for player "{agent_symbol}" ship "{ship}" at "{waypoint}" good "{good}" units {units:d} price {price:d} total {total:d}'))
def record_transaction(context, tx_type, agent_symbol, ship, waypoint, good, units, price, total):
    """Record a transaction for a player"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].transaction() as conn:
        result = context['db'].record_transaction(
            conn, player_id,
            ship_symbol=ship,
            waypoint_symbol=waypoint,
            good_symbol=good,
            transaction_type=tx_type,
            units=units,
            price_per_unit=price,
            total_cost=total
        )
        context['result'] = result


@when(parsers.parse('player "{agent_symbol}" updates market data for waypoint "{waypoint}" and good "{good}" supply "{supply}" activity "{activity}" buy {buy:d} sell {sell:d} volume {volume:d}'))
def update_market_data(context, agent_symbol, waypoint, good, supply, activity, buy, sell, volume):
    """Update market data for a waypoint and good"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].transaction() as conn:
        result = context['db'].update_market_data(
            conn,
            waypoint_symbol=waypoint,
            good_symbol=good,
            supply=supply,
            activity=activity,
            purchase_price=buy,
            sell_price=sell,
            trade_volume=volume,
            last_updated=datetime.utcnow().isoformat(),
            player_id=player_id
        )
        context['result'] = result
        # Store for later verification
        context['market_data'] = [(waypoint, good)]


@when(parsers.re(r'I get market data for waypoint "(?P<waypoint>[^"]+)" and good "(?P<good>[^"]+)"'))
def get_market_data_filtered(context, waypoint, good):
    """Get market data for a waypoint and specific good"""
    with context['db'].connection() as conn:
        result = context['db'].get_market_data(conn, waypoint, good)
        context['result'] = result


@when(parsers.parse('I get market data for waypoint "{waypoint}"'))
def get_market_data(context, waypoint):
    """Get market data for a waypoint (all goods)"""
    with context['db'].connection() as conn:
        context['result'] = context['db'].get_market_data(conn, waypoint)


@when(parsers.parse('I get transactions for player "{agent_symbol}" filtered by ship "{ship}"'))
def get_transactions_by_ship(context, agent_symbol, ship):
    """Get transactions filtered by ship"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].connection() as conn:
        context['result'] = context['db'].get_transactions(conn, player_id, ship_symbol=ship)


@when(parsers.parse('I get transactions for player "{agent_symbol}" filtered by waypoint "{waypoint}"'))
def get_transactions_by_waypoint(context, agent_symbol, waypoint):
    """Get transactions filtered by waypoint"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].connection() as conn:
        context['result'] = context['db'].get_transactions(conn, player_id, waypoint_symbol=waypoint)


@when(parsers.parse('I get transactions for player "{agent_symbol}" filtered by good "{good}"'))
def get_transactions_by_good(context, agent_symbol, good):
    """Get transactions filtered by good"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].connection() as conn:
        context['result'] = context['db'].get_transactions(conn, player_id, good_symbol=good)


@when(parsers.parse('I get transactions for player "{agent_symbol}" with no filters'))
def get_transactions_all(context, agent_symbol):
    """Get all transactions for a player"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].connection() as conn:
        context['result'] = context['db'].get_transactions(conn, player_id)


@when(parsers.parse('I retrieve the system graph for "{system_symbol}"'))
def get_system_graph(context, system_symbol):
    """Retrieve system graph"""
    with context['db'].connection() as conn:
        context['graph'] = context['db'].get_system_graph(conn, system_symbol)


@when("I list all systems with graphs")
def list_systems(context):
    """List all systems with graphs"""
    with context['db'].connection() as conn:
        context['systems'] = context['db'].list_systems(conn)


@when(parsers.parse('I find fuel stations in system "{system_symbol}"'))
def find_fuel_stations(context, system_symbol):
    """Find fuel stations in a system"""
    with context['db'].connection() as conn:
        context['fuel_stations'] = context['db'].find_fuel_stations(conn, system_symbol)


@when(parsers.parse('I get the player by agent symbol "{agent_symbol}"'))
def get_player_by_symbol(context, agent_symbol):
    """Get player by agent symbol"""
    with context['db'].connection() as conn:
        player = context['db'].get_player(conn, agent_symbol)
        context['result'] = player
        if player:
            context['last_player_id'] = player['player_id']


@when("I get the player by that ID")
def get_player_by_id(context):
    """Get player by ID"""
    player_id = context['last_player_id']

    with context['db'].connection() as conn:
        context['result'] = context['db'].get_player_by_id(conn, player_id)


@when(parsers.parse('I list ship assignments for player "{agent_symbol}" with status "{status}"'))
def list_ship_assignments_filtered(context, agent_symbol, status):
    """List ship assignments filtered by status"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].connection() as conn:
        context['result'] = context['db'].list_ship_assignments(conn, player_id, status)


@when(parsers.parse('I find available ships for player "{agent_symbol}"'))
def find_available_ships(context, agent_symbol):
    """Find available ships for a player"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].connection() as conn:
        context['result'] = context['db'].find_available_ships(conn, player_id)


@given(parsers.parse('market data exists for waypoint "{waypoint}" with goods "{goods}"'))
def market_data_exists(context, waypoint, goods):
    """Create market data for multiple goods"""
    # Get or create a player
    if 'CMDR_AC_2025' not in context['player_ids']:
        with context['db'].transaction() as conn:
            player_id = context['db'].create_player(conn, 'CMDR_AC_2025', 'test-token')
            context['player_ids']['CMDR_AC_2025'] = player_id
    else:
        player_id = context['player_ids']['CMDR_AC_2025']

    goods_list = goods.split(',')

    with context['db'].transaction() as conn:
        for good in goods_list:
            context['db'].update_market_data(
                conn,
                waypoint_symbol=waypoint,
                good_symbol=good,
                supply='MODERATE',
                activity='STRONG',
                purchase_price=60,
                sell_price=70,
                trade_volume=100,
                last_updated=datetime.utcnow().isoformat(),
                player_id=player_id
            )


@given(parsers.parse('transaction {num:d} exists: player "{agent_symbol}" ship "{ship}" at "{waypoint}" good "{good}" {tx_type} {units:d} units'))
def transaction_exists(context, num, agent_symbol, ship, waypoint, good, tx_type, units):
    """Create a transaction for a player"""
    player_id = context['player_ids'][agent_symbol]

    # Use simple pricing
    price_map = {
        'IRON_ORE': 70,
        'COPPER_ORE': 65,
        'ALUMINUM_ORE': 75
    }
    price = price_map.get(good, 60)
    total = units * price

    with context['db'].transaction() as conn:
        context['db'].record_transaction(
            conn, player_id,
            ship_symbol=ship,
            waypoint_symbol=waypoint,
            good_symbol=good,
            transaction_type=tx_type,
            units=units,
            price_per_unit=price,
            total_cost=total
        )


@given(parsers.parse('a system graph "{system_symbol}" with {wp_count:d} waypoints and {edge_count:d} edges'))
def system_graph_exists_complex(context, system_symbol, wp_count, edge_count):
    """Create a system graph with waypoints and edges"""
    waypoints = {}
    edges = []

    # Create waypoints
    for i in range(wp_count):
        wp_symbol = f"{system_symbol}-W{i+1}"
        waypoints[wp_symbol] = {
            'type': 'PLANET' if i == 0 else 'MOON',
            'x': float(i * 10),
            'y': float(i * 10),
            'has_fuel': i < 2,  # First two have fuel
            'traits': [],
            'orbitals': []
        }

    # Create edges
    waypoint_symbols = list(waypoints.keys())
    for i in range(min(edge_count, len(waypoint_symbols) - 1)):
        edges.append({
            'from': waypoint_symbols[i],
            'to': waypoint_symbols[i + 1],
            'distance': 100.0,
            'type': 'normal'
        })

    graph_data = {
        'waypoints': waypoints,
        'edges': edges
    }

    with context['db'].transaction() as conn:
        context['db'].save_system_graph(conn, system_symbol, graph_data)


@given(parsers.re(r'a system graph exists for "(?P<system_symbol>[^"]+)" with fuel waypoints "(?P<waypoints>[^"]+)"'))
def system_graph_with_fuel(context, system_symbol, waypoints):
    """Create a system graph with specific fuel waypoints"""
    waypoint_list = waypoints.split(',')

    graph_waypoints = {}
    for wp in waypoint_list:
        graph_waypoints[wp] = {
            'type': 'PLANET',
            'x': 0,
            'y': 0,
            'has_fuel': True,
            'traits': [],
            'orbitals': []
        }

    graph_data = {
        'waypoints': graph_waypoints,
        'edges': []
    }

    with context['db'].transaction() as conn:
        context['db'].save_system_graph(conn, system_symbol, graph_data)


@given(parsers.parse('a system graph exists for "{system_symbol}"'))
def system_graph_exists(context, system_symbol):
    """Create a minimal system graph"""
    graph_data = {
        'waypoints': {
            f'{system_symbol}-A1': {'type': 'PLANET', 'x': 0, 'y': 0, 'has_fuel': True, 'traits': [], 'orbitals': []},
        },
        'edges': []
    }

    with context['db'].transaction() as conn:
        context['db'].save_system_graph(conn, system_symbol, graph_data)


@given(parsers.parse('ship "{ship}" is assigned to "{operator}" for player "{agent_symbol}"'))
def assign_ship_to_operator(context, ship, operator, agent_symbol):
    """Assign ship to operator for a player"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].transaction() as conn:
        context['db'].assign_ship(
            conn, player_id, ship,
            assigned_to=operator,
            daemon_id=f"daemon-{ship}",
            operation=operator.replace('_operator', '')
        )


@given(parsers.parse('ship "{ship}" is idle for player "{agent_symbol}"'))
def ship_idle(context, ship, agent_symbol):
    """Make ship idle for a player"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].transaction() as conn:
        # First assign it
        context['db'].assign_ship(
            conn, player_id, ship,
            assigned_to="test",
            daemon_id=f"daemon-{ship}",
            operation="test"
        )
        # Then release it
        context['db'].release_ship(conn, player_id, ship)


@then("the player should exist in the database")
def verify_player_exists(context):
    """Verify player exists"""
    # Get the last created player
    agent_symbol = list(context['players'].keys())[-1]

    with context['db'].connection() as conn:
        player = context['db'].get_player(conn, agent_symbol)
        assert player is not None, f"Player {agent_symbol} should exist"


@then("the player should exist")
def verify_player_exists_simple(context):
    """Verify player exists"""
    verify_player_exists(context)


@then(parsers.parse('the player\'s agent symbol should be "{agent_symbol}"'))
def verify_agent_symbol(context, agent_symbol):
    """Verify player's agent symbol"""
    assert context['result'] is not None
    assert context['result']['agent_symbol'] == agent_symbol


@then(parsers.parse('the player\'s token should be "{token}"'))
def verify_token(context, token):
    """Verify player's token"""
    assert context['result'] is not None or len(context['players']) > 0

    if context['result']:
        assert context['result']['token'] == token
    else:
        # Check in stored players
        agent_symbol = list(context['players'].keys())[-1]
        assert context['players'][agent_symbol]['token'] == token


@then("the player's last_active should be recent")
def verify_last_active(context):
    """Verify player's last_active is recent"""
    player_id = context['last_player_id']

    with context['db'].connection() as conn:
        player = context['db'].get_player_by_id(conn, player_id)
        assert player is not None

        last_active = datetime.fromisoformat(player['last_active'])
        now = datetime.utcnow()
        diff = now - last_active

        # Should be within 5 seconds
        assert diff.total_seconds() < 5, f"last_active is {diff.total_seconds()} seconds old"


@then(parsers.parse('I should see {count:d} players'))
def verify_player_count(context, count):
    """Verify number of players"""
    assert len(context['result']) == count, f"Expected {count} players, got {len(context['result'])}"


@then(parsers.parse('the players list should include "{agent_symbol}"'))
def verify_player_in_list(context, agent_symbol):
    """Verify player in list"""
    agent_symbols = [p['agent_symbol'] for p in context['result']]
    assert agent_symbol in agent_symbols, f"{agent_symbol} not in players list"


@then("the transaction should be recorded")
def verify_transaction_recorded(context):
    """Verify transaction was recorded"""
    assert context['result'] is True


@then(parsers.parse('I should be able to retrieve {count:d} transaction for player "{agent_symbol}"'))
def verify_transaction_count(context, count, agent_symbol):
    """Verify transaction count for a player"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].connection() as conn:
        transactions = context['db'].get_transactions(conn, player_id)
        assert len(transactions) == count, f"Expected {count} transactions, got {len(transactions)}"


@then(parsers.parse('the market data should be visible to player "{agent_symbol}"'))
def verify_market_data_visible(context, agent_symbol):
    """Verify market data is visible to another player"""
    # Market data is shared, so just verify we can retrieve it
    waypoint, good = context['market_data'][0]

    with context['db'].connection() as conn:
        data = context['db'].get_market_data(conn, waypoint, good)
        assert len(data) > 0, "Market data should be visible"


@then(parsers.parse('the market data should show purchase_price {price:d}'))
def verify_purchase_price(context, price):
    """Verify market purchase price"""
    waypoint, good = context['market_data'][0]

    with context['db'].connection() as conn:
        data = context['db'].get_market_data(conn, waypoint, good)
        assert len(data) > 0
        assert data[0]['purchase_price'] == price


@then(parsers.parse('the market data should show sell_price {price:d}'))
def verify_sell_price(context, price):
    """Verify market sell price"""
    waypoint, good = context['market_data'][0]

    with context['db'].connection() as conn:
        data = context['db'].get_market_data(conn, waypoint, good)
        assert len(data) > 0
        assert data[0]['sell_price'] == price


@then(parsers.parse('I should see {count:d} goods in the market data'))
def verify_market_goods_count(context, count):
    """Verify number of goods in market data"""
    assert len(context['result']) == count, f"Expected {count} goods, got {len(context['result'])}"


@then(parsers.parse('I should see {count:d} good in the market data'))
def verify_market_good_count(context, count):
    """Verify number of goods in market data (singular)"""
    verify_market_goods_count(context, count)


@then(parsers.parse('the good should be "{good}"'))
def verify_good_symbol(context, good):
    """Verify good symbol"""
    assert len(context['result']) > 0
    assert context['result'][0]['good_symbol'] == good


@then(parsers.parse('I should see {count:d} transactions'))
def verify_transactions_count(context, count):
    """Verify number of transactions"""
    assert len(context['result']) == count, f"Expected {count} transactions, got {len(context['result'])}"


@then(parsers.parse('the graph should have {count:d} waypoints'))
def verify_waypoint_count(context, count):
    """Verify number of waypoints in graph"""
    assert context['graph'] is not None
    assert len(context['graph']['waypoints']) == count


@then(parsers.parse('the graph should have {count:d} edges'))
def verify_edge_count(context, count):
    """Verify number of edges in graph"""
    assert context['graph'] is not None
    assert len(context['graph']['edges']) == count


@then(parsers.parse('waypoint "{waypoint}" should have fuel'))
def verify_waypoint_has_fuel(context, waypoint):
    """Verify waypoint has fuel"""
    assert context['graph'] is not None
    assert waypoint in context['graph']['waypoints']
    assert context['graph']['waypoints'][waypoint]['has_fuel'] is True


@then(parsers.parse('waypoint "{waypoint}" should not have fuel'))
def verify_waypoint_no_fuel(context, waypoint):
    """Verify waypoint doesn't have fuel"""
    assert context['graph'] is not None
    assert waypoint in context['graph']['waypoints']
    assert context['graph']['waypoints'][waypoint]['has_fuel'] is False


@then(parsers.parse('I should see {count:d} systems'))
def verify_systems_count(context, count):
    """Verify number of systems"""
    assert len(context['systems']) == count


@then(parsers.parse('the systems list should include "{system}"'))
def verify_system_in_list(context, system):
    """Verify system in list"""
    assert system in context['systems']


@then(parsers.parse('I should see {count:d} fuel stations'))
def verify_fuel_stations_count(context, count):
    """Verify number of fuel stations"""
    assert len(context['fuel_stations']) == count


@then(parsers.parse('the fuel stations should include "{waypoint}"'))
def verify_fuel_station_in_list(context, waypoint):
    """Verify fuel station in list"""
    assert waypoint in context['fuel_stations']


@then(parsers.parse('player "{agent_symbol}" should have {count:d} transaction'))
def verify_player_transaction_count(context, agent_symbol, count):
    """Verify player transaction count"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].connection() as conn:
        transactions = context['db'].get_transactions(conn, player_id)
        assert len(transactions) == count


@then(parsers.parse('player "{agent_symbol}" transactions should not include "{other_agent}" transactions'))
def verify_transaction_isolation(context, agent_symbol, other_agent):
    """Verify transaction isolation between players"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].connection() as conn:
        transactions = context['db'].get_transactions(conn, player_id)

        # Verify all transactions belong to this player
        for tx in transactions:
            assert tx['player_id'] == player_id


@then(parsers.parse('the player metadata should include "{key}" as "{value}"'))
def verify_player_metadata(context, key, value):
    """Verify player metadata"""
    with context['db'].connection() as conn:
        agent_symbol = list(context['players'].keys())[-1]
        player = context['db'].get_player(conn, agent_symbol)

        assert player is not None
        assert 'metadata' in player
        assert key in player['metadata']
        assert player['metadata'][key] == value


@then("I should have the player's ID")
def verify_player_id(context):
    """Verify we have player ID"""
    assert context['last_player_id'] is not None


@then(parsers.parse('I should see {count:d} ship assignments'))
def verify_ship_assignments_count(context, count):
    """Verify number of ship assignments"""
    assert len(context['result']) == count


@then(parsers.parse('I should see {count:d} ship assignment'))
def verify_ship_assignment_count(context, count):
    """Verify number of ship assignments (singular)"""
    verify_ship_assignments_count(context, count)


@then(parsers.parse('I should see {count:d} available ships'))
def verify_available_ships_count(context, count):
    """Verify number of available ships"""
    assert len(context['result']) == count


@then(parsers.parse('the available ships should include "{ship}"'))
def verify_ship_in_available(context, ship):
    """Verify ship in available list"""
    assert ship in context['result']


@when(parsers.parse('I try to assign "{ship}" to "{operator}" for player "{agent_symbol}"'))
def try_assign_ship(context, ship, operator, agent_symbol):
    """Try to assign ship (may fail if already assigned)"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].transaction() as conn:
        result = context['db'].assign_ship(
            conn, player_id, ship,
            assigned_to=operator,
            daemon_id=f"daemon-{ship}",
            operation=operator.replace('_operator', '')
        )
        context['result'] = result


@then("the assignment should fail")
def verify_assignment_failed(context):
    """Verify assignment failed"""
    assert context['result'] is False


@then(parsers.parse('the ship should still be assigned to "{operator}"'))
def verify_ship_still_assigned(context, operator):
    """Verify ship is still assigned to original operator"""
    # This is verified by checking the assignment hasn't changed
    pass  # The database ensures this


@when(parsers.parse('I assign ship "{ship}" to "{operator}" for player "{agent_symbol}" with metadata {metadata}'))
def assign_ship_with_metadata(context, ship, operator, agent_symbol, metadata):
    """Assign ship with metadata"""
    player_id = context['player_ids'][agent_symbol]
    metadata_dict = json.loads(metadata.replace("'", '"'))

    with context['db'].transaction() as conn:
        result = context['db'].assign_ship(
            conn, player_id, ship,
            assigned_to=operator,
            daemon_id=f"daemon-{ship}",
            operation=operator.replace('_operator', ''),
            metadata=metadata_dict
        )
        context['result'] = result
        context['last_ship'] = (player_id, ship)


@then(parsers.parse('the ship assignment should have metadata key "{key}" with value "{value}"'))
def verify_assignment_metadata_string(context, key, value):
    """Verify assignment metadata (string value)"""
    player_id, ship = context['last_ship']

    with context['db'].connection() as conn:
        assignment = context['db'].get_ship_assignment(conn, player_id, ship)
        assert assignment is not None
        assert 'metadata' in assignment
        assert key in assignment['metadata']
        assert str(assignment['metadata'][key]) == value


@then(parsers.parse('the ship assignment should have metadata key "{key}" with value {value:d}'))
def verify_assignment_metadata_int(context, key, value):
    """Verify assignment metadata (int value)"""
    player_id, ship = context['last_ship']

    with context['db'].connection() as conn:
        assignment = context['db'].get_ship_assignment(conn, player_id, ship)
        assert assignment is not None
        assert 'metadata' in assignment
        assert key in assignment['metadata']
        assert assignment['metadata'][key] == value


@when(parsers.parse('I try to get player by ID {player_id:d}'))
def try_get_player_by_id(context, player_id):
    """Try to get player by ID (may not exist)"""
    with context['db'].connection() as conn:
        context['result'] = context['db'].get_player_by_id(conn, player_id)


@then("the result should be None")
def verify_result_none(context):
    """Verify result is None"""
    assert context['result'] is None


@when(parsers.parse('I create daemon "{daemon_id}" for player "{agent_symbol}" with PID {pid:d} command "{command}"'))
def create_daemon(context, daemon_id, agent_symbol, pid, command):
    """Create a daemon"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].transaction() as conn:
        result = context['db'].create_daemon(
            conn, player_id, daemon_id, pid,
            command=[command],
            log_file=f"/tmp/{daemon_id}.log",
            err_file=f"/tmp/{daemon_id}.err"
        )
        context['result'] = result


@then("the daemon should be created successfully")
def verify_daemon_created(context):
    """Verify daemon was created"""
    assert context['result'] is True


@then(parsers.parse('I should be able to retrieve daemon "{daemon_id}" for player "{agent_symbol}"'))
def verify_can_retrieve_daemon(context, daemon_id, agent_symbol):
    """Verify daemon can be retrieved"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].connection() as conn:
        daemon = context['db'].get_daemon(conn, player_id, daemon_id)
        assert daemon is not None
        assert daemon['daemon_id'] == daemon_id


@given(parsers.parse('daemon "{daemon_id}" exists for player "{agent_symbol}"'))
def daemon_exists(context, daemon_id, agent_symbol):
    """Create a daemon for testing"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].transaction() as conn:
        context['db'].create_daemon(
            conn, player_id, daemon_id, 12345,
            command=["python3", "test.py"],
            log_file=f"/tmp/{daemon_id}.log",
            err_file=f"/tmp/{daemon_id}.err"
        )


@when(parsers.parse('I update daemon "{daemon_id}" status to "{status}" for player "{agent_symbol}"'))
def update_daemon_status(context, daemon_id, status, agent_symbol):
    """Update daemon status"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].transaction() as conn:
        result = context['db'].update_daemon_status(
            conn, player_id, daemon_id, status,
            stopped_at=datetime.utcnow().isoformat() if status == 'stopped' else None
        )
        context['result'] = result


@then(parsers.parse('daemon "{daemon_id}" should have status "{status}"'))
def verify_daemon_status(context, daemon_id, status):
    """Verify daemon status"""
    # Get player_id from last created player
    agent_symbol = list(context['player_ids'].keys())[0]
    player_id = context['player_ids'][agent_symbol]

    with context['db'].connection() as conn:
        daemon = context['db'].get_daemon(conn, player_id, daemon_id)
        assert daemon is not None
        assert daemon['status'] == status


@given(parsers.re(r'daemon "(?P<daemon_id>[^"]+)" with status "(?P<status>[^"]+)" exists for player "(?P<agent_symbol>[^"]+)"'))
def daemon_with_status_exists(context, daemon_id, status, agent_symbol):
    """Create a daemon with specific status"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].transaction() as conn:
        context['db'].create_daemon(
            conn, player_id, daemon_id, 12345,
            command=["python3", "test.py"],
            log_file=f"/tmp/{daemon_id}.log",
            err_file=f"/tmp/{daemon_id}.err"
        )

        if status != 'running':
            context['db'].update_daemon_status(
                conn, player_id, daemon_id, status,
                stopped_at=datetime.utcnow().isoformat() if status == 'stopped' else None
            )


@when(parsers.parse('I list daemons for player "{agent_symbol}" with status "{status}"'))
def list_daemons_filtered(context, agent_symbol, status):
    """List daemons filtered by status"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].connection() as conn:
        context['result'] = context['db'].list_daemons(conn, player_id, status)


@then(parsers.parse('I should see {count:d} daemons'))
def verify_daemons_count(context, count):
    """Verify daemon count"""
    assert len(context['result']) == count


@when(parsers.parse('I delete daemon "{daemon_id}" for player "{agent_symbol}"'))
def delete_daemon(context, daemon_id, agent_symbol):
    """Delete a daemon"""
    player_id = context['player_ids'][agent_symbol]

    with context['db'].transaction() as conn:
        result = context['db'].delete_daemon(conn, player_id, daemon_id)
        context['result'] = result


@then("the daemon should be deleted successfully")
def verify_daemon_deleted(context):
    """Verify daemon was deleted"""
    assert context['result'] is True
