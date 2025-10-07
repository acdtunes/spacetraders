#!/usr/bin/env python3
from __future__ import annotations

"""
Database Layer - SQLite with WAL mode for multi-agent concurrency

Provides centralized data storage for:
- Ship assignments
- Daemon management
- System graphs
- Market data & transactions
- Operation status

Author: Claude Code
"""

import json
import logging
import sqlite3
from contextlib import contextmanager
from datetime import datetime
from pathlib import Path
from typing import Any, Dict, List, Optional

from ..helpers import paths


logger = logging.getLogger(__name__)


class Database:
    """
    SQLite database manager with WAL mode for concurrent access

    Usage:
        db = Database()

        # Transaction example
        with db.transaction() as conn:
            db.assign_ship(conn, "SHIP-1", "operator", "daemon-1", "mine")
            db.create_daemon(conn, "daemon-1", 12345, ["python3", "bot.py"])
    """

    def __init__(self, db_path: str | Path | None = None):
        """
        Initialize database connection

        Args:
            db_path: Path to SQLite database file
        """
        self.db_path = Path(db_path) if db_path else paths.sqlite_path()
        self.db_path.parent.mkdir(parents=True, exist_ok=True)

        # Initialize database and enable WAL mode
        self._init_database()

        logger.info(f"Database initialized at {self.db_path}")

    def _get_connection(self) -> sqlite3.Connection:
        """Get database connection with optimized settings"""
        conn = sqlite3.connect(
            str(self.db_path),
            check_same_thread=False,  # Allow multi-threading
            timeout=30.0  # Wait up to 30s for locks
        )

        # Enable WAL mode for better concurrency
        conn.execute('PRAGMA journal_mode=WAL')

        # Enable foreign keys
        conn.execute('PRAGMA foreign_keys=ON')

        # Return rows as dictionaries
        conn.row_factory = sqlite3.Row

        return conn

    def _init_database(self):
        """Initialize database schema"""
        with self._get_connection() as conn:
            cursor = conn.cursor()

            # =====================================================
            # PLAYERS (Multi-tenancy)
            # =====================================================
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS players (
                    player_id INTEGER PRIMARY KEY AUTOINCREMENT,
                    agent_symbol TEXT UNIQUE NOT NULL,
                    token TEXT NOT NULL,
                    created_at TIMESTAMP NOT NULL,
                    last_active TIMESTAMP,
                    metadata TEXT
                )
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_player_agent
                ON players(agent_symbol)
            """)

            # =====================================================
            # SHIP ASSIGNMENTS (Per-player)
            # =====================================================
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS ship_assignments (
                    ship_symbol TEXT NOT NULL,
                    player_id INTEGER NOT NULL,
                    assigned_to TEXT,
                    daemon_id TEXT,
                    operation TEXT,
                    status TEXT NOT NULL DEFAULT 'idle',
                    assigned_at TIMESTAMP,
                    released_at TIMESTAMP,
                    release_reason TEXT,
                    metadata TEXT,
                    PRIMARY KEY (ship_symbol, player_id),
                    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
                )
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_ship_status
                ON ship_assignments(status)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_ship_daemon
                ON ship_assignments(daemon_id)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_ship_player
                ON ship_assignments(player_id)
            """)

            # =====================================================
            # DAEMONS (Per-player)
            # =====================================================
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS daemons (
                    daemon_id TEXT NOT NULL,
                    player_id INTEGER NOT NULL,
                    pid INTEGER,
                    command TEXT NOT NULL,
                    started_at TIMESTAMP NOT NULL,
                    stopped_at TIMESTAMP,
                    status TEXT NOT NULL DEFAULT 'running',
                    log_file TEXT,
                    err_file TEXT,
                    PRIMARY KEY (daemon_id, player_id),
                    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
                )
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_daemon_status
                ON daemons(status)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_daemon_pid
                ON daemons(pid)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_daemon_player
                ON daemons(player_id)
            """)

            # =====================================================
            # SYSTEM GRAPHS (SHARED - all players see same universe)
            # =====================================================
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS system_graphs (
                    system_symbol TEXT PRIMARY KEY,
                    graph_data TEXT NOT NULL,
                    created_at TIMESTAMP NOT NULL,
                    updated_at TIMESTAMP NOT NULL
                )
            """)

            # =====================================================
            # WAYPOINTS (normalized)
            # =====================================================
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS waypoints (
                    waypoint_symbol TEXT PRIMARY KEY,
                    system_symbol TEXT NOT NULL,
                    type TEXT NOT NULL,
                    x REAL NOT NULL,
                    y REAL NOT NULL,
                    traits TEXT,
                    has_fuel INTEGER NOT NULL DEFAULT 0,
                    orbitals TEXT
                )
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_waypoint_system
                ON waypoints(system_symbol)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_waypoint_fuel
                ON waypoints(has_fuel)
            """)

            # =====================================================
            # GRAPH EDGES (normalized)
            # =====================================================
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS graph_edges (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    system_symbol TEXT NOT NULL,
                    from_waypoint TEXT NOT NULL,
                    to_waypoint TEXT NOT NULL,
                    distance REAL NOT NULL,
                    edge_type TEXT NOT NULL DEFAULT 'normal'
                )
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_edge_system
                ON graph_edges(system_symbol)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_edge_from
                ON graph_edges(from_waypoint)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_edge_to
                ON graph_edges(to_waypoint)
            """)

            # =====================================================
            # MARKET DATA (SHARED - all players see same markets)
            # =====================================================
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS market_data (
                    waypoint_symbol TEXT NOT NULL,
                    good_symbol TEXT NOT NULL,
                    supply TEXT,
                    activity TEXT,
                    purchase_price INTEGER,
                    sell_price INTEGER,
                    trade_volume INTEGER,
                    last_updated TIMESTAMP NOT NULL,
                    updated_by_player INTEGER,
                    PRIMARY KEY (waypoint_symbol, good_symbol),
                    FOREIGN KEY (updated_by_player) REFERENCES players(player_id) ON DELETE SET NULL
                )
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_market_waypoint
                ON market_data(waypoint_symbol)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_market_good
                ON market_data(good_symbol)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_market_updated
                ON market_data(last_updated)
            """)

            # =====================================================
            # MARKET TRANSACTIONS (Per-player historical)
            # =====================================================
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS market_transactions (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    player_id INTEGER NOT NULL,
                    ship_symbol TEXT NOT NULL,
                    waypoint_symbol TEXT NOT NULL,
                    good_symbol TEXT NOT NULL,
                    transaction_type TEXT NOT NULL,
                    units INTEGER NOT NULL,
                    price_per_unit INTEGER NOT NULL,
                    total_cost INTEGER NOT NULL,
                    timestamp TIMESTAMP NOT NULL,
                    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
                )
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_transaction_ship
                ON market_transactions(ship_symbol)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_transaction_waypoint
                ON market_transactions(waypoint_symbol)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_transaction_good
                ON market_transactions(good_symbol)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_transaction_time
                ON market_transactions(timestamp)
            """)

            # =====================================================
            # TOUR CACHE (SHARED - all players benefit from optimizations)
            # =====================================================
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS tour_cache (
                    system TEXT NOT NULL,
                    markets TEXT NOT NULL,
                    algorithm TEXT NOT NULL,
                    start_waypoint TEXT,
                    tour_order TEXT NOT NULL,
                    total_distance REAL NOT NULL,
                    calculated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
                    PRIMARY KEY (system, markets, algorithm, start_waypoint)
                )
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_tour_system
                ON tour_cache(system)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_tour_algorithm
                ON tour_cache(algorithm)
            """)

            conn.commit()

    @contextmanager
    def transaction(self):
        """
        Context manager for database transactions

        Usage:
            with db.transaction() as conn:
                db.assign_ship(conn, ...)
                db.create_daemon(conn, ...)
        """
        conn = self._get_connection()
        try:
            yield conn
            conn.commit()
        except Exception as e:
            conn.rollback()
            logger.error(f"Transaction failed: {e}")
            raise
        finally:
            conn.close()

    @contextmanager
    def connection(self):
        """
        Context manager for read-only operations

        Usage:
            with db.connection() as conn:
                result = db.get_ship_assignment(conn, "SHIP-1")
        """
        conn = self._get_connection()
        try:
            yield conn
        finally:
            conn.close()

    # =========================================================================
    # PLAYERS
    # =========================================================================

    def create_player(self, conn: sqlite3.Connection, agent_symbol: str,
                     token: str, metadata: Optional[Dict] = None) -> int:
        """
        Create or update player record

        Args:
            conn: Database connection
            agent_symbol: Agent symbol (e.g., "CMDR_AC_2025")
            token: API token
            metadata: Optional metadata dict

        Returns:
            player_id
        """
        cursor = conn.cursor()

        cursor.execute("""
            INSERT INTO players (agent_symbol, token, created_at, last_active, metadata)
            VALUES (?, ?, ?, ?, ?)
            ON CONFLICT(agent_symbol) DO UPDATE SET
                token = excluded.token,
                last_active = excluded.last_active,
                metadata = excluded.metadata
            RETURNING player_id
        """, (agent_symbol, token, datetime.utcnow().isoformat(),
              datetime.utcnow().isoformat(), json.dumps(metadata or {})))

        result = cursor.fetchone()
        return result['player_id']

    def get_player(self, conn: sqlite3.Connection, agent_symbol: str) -> Optional[Dict]:
        """Get player by agent symbol"""
        cursor = conn.cursor()

        cursor.execute("""
            SELECT * FROM players WHERE agent_symbol = ?
        """, (agent_symbol,))

        row = cursor.fetchone()
        if row:
            result = dict(row)
            if result.get('metadata'):
                result['metadata'] = json.loads(result['metadata'])
            return result
        return None

    def get_player_by_id(self, conn: sqlite3.Connection, player_id: int) -> Optional[Dict]:
        """Get player by ID"""
        cursor = conn.cursor()

        cursor.execute("""
            SELECT * FROM players WHERE player_id = ?
        """, (player_id,))

        row = cursor.fetchone()
        if row:
            result = dict(row)
            if result.get('metadata'):
                result['metadata'] = json.loads(result['metadata'])
            return result
        return None

    def list_players(self, conn: sqlite3.Connection) -> List[Dict]:
        """List all players"""
        cursor = conn.cursor()

        cursor.execute("""
            SELECT * FROM players ORDER BY agent_symbol
        """)

        results = []
        for row in cursor.fetchall():
            result = dict(row)
            if result.get('metadata'):
                result['metadata'] = json.loads(result['metadata'])
            results.append(result)

        return results

    def update_player_activity(self, conn: sqlite3.Connection, player_id: int):
        """Update player's last_active timestamp"""
        cursor = conn.cursor()

        cursor.execute("""
            UPDATE players SET last_active = ? WHERE player_id = ?
        """, (datetime.utcnow().isoformat(), player_id))

    # =========================================================================
    # SHIP ASSIGNMENTS
    # =========================================================================

    def assign_ship(self, conn: sqlite3.Connection, player_id: int, ship_symbol: str,
                   assigned_to: str, daemon_id: str, operation: str,
                   metadata: Optional[Dict] = None) -> bool:
        """
        Assign ship to operation (atomic with row locking)

        Args:
            conn: Database connection
            player_id: Player ID
            ship_symbol: Ship symbol
            assigned_to: Operator name
            daemon_id: Daemon ID
            operation: Operation type
            metadata: Optional metadata dict

        Returns:
            True if assigned successfully
        """
        cursor = conn.cursor()

        # Check if ship exists and lock row
        cursor.execute("""
            SELECT ship_symbol, status, daemon_id
            FROM ship_assignments
            WHERE ship_symbol = ? AND player_id = ?
        """, (ship_symbol, player_id))

        existing = cursor.fetchone()

        if existing and existing['status'] == 'active':
            logger.warning(f"Ship {ship_symbol} already assigned for player {player_id}")
            return False

        # Upsert assignment
        cursor.execute("""
            INSERT INTO ship_assignments
                (ship_symbol, player_id, assigned_to, daemon_id, operation, status, assigned_at, metadata)
            VALUES (?, ?, ?, ?, ?, 'active', ?, ?)
            ON CONFLICT(ship_symbol, player_id) DO UPDATE SET
                assigned_to = excluded.assigned_to,
                daemon_id = excluded.daemon_id,
                operation = excluded.operation,
                status = 'active',
                assigned_at = excluded.assigned_at,
                metadata = excluded.metadata,
                released_at = NULL,
                release_reason = NULL
        """, (ship_symbol, player_id, assigned_to, daemon_id, operation,
              datetime.utcnow().isoformat(),
              json.dumps(metadata or {})))

        return True

    def release_ship(self, conn: sqlite3.Connection, player_id: int, ship_symbol: str,
                    reason: str = "operation_complete") -> bool:
        """Release ship from assignment"""
        cursor = conn.cursor()

        cursor.execute("""
            UPDATE ship_assignments
            SET status = 'idle',
                assigned_to = NULL,
                daemon_id = NULL,
                operation = NULL,
                released_at = ?,
                release_reason = ?
            WHERE ship_symbol = ? AND player_id = ?
        """, (datetime.utcnow().isoformat(), reason, ship_symbol, player_id))

        return cursor.rowcount > 0

    def get_ship_assignment(self, conn: sqlite3.Connection, player_id: int,
                           ship_symbol: str) -> Optional[Dict]:
        """Get ship assignment details"""
        cursor = conn.cursor()

        cursor.execute("""
            SELECT * FROM ship_assignments WHERE ship_symbol = ? AND player_id = ?
        """, (ship_symbol, player_id))

        row = cursor.fetchone()
        if row:
            result = dict(row)
            if result.get('metadata'):
                result['metadata'] = json.loads(result['metadata'])
            return result
        return None

    def list_ship_assignments(self, conn: sqlite3.Connection, player_id: int,
                            status: Optional[str] = None) -> List[Dict]:
        """List all ship assignments for a player, optionally filtered by status"""
        cursor = conn.cursor()

        if status:
            cursor.execute("""
                SELECT * FROM ship_assignments WHERE player_id = ? AND status = ?
                ORDER BY assigned_at DESC
            """, (player_id, status))
        else:
            cursor.execute("""
                SELECT * FROM ship_assignments WHERE player_id = ?
                ORDER BY assigned_at DESC
            """, (player_id,))

        results = []
        for row in cursor.fetchall():
            result = dict(row)
            if result.get('metadata'):
                result['metadata'] = json.loads(result['metadata'])
            results.append(result)

        return results

    def find_available_ships(self, conn: sqlite3.Connection, player_id: int) -> List[str]:
        """Find all available (idle) ships for a player"""
        cursor = conn.cursor()

        cursor.execute("""
            SELECT ship_symbol FROM ship_assignments
            WHERE player_id = ? AND status = 'idle'
        """, (player_id,))

        return [row['ship_symbol'] for row in cursor.fetchall()]

    # =========================================================================
    # DAEMONS
    # =========================================================================

    def create_daemon(self, conn: sqlite3.Connection, player_id: int, daemon_id: str,
                     pid: int, command: List[str], log_file: str,
                     err_file: str) -> bool:
        """Create daemon record"""
        cursor = conn.cursor()

        cursor.execute("""
            INSERT INTO daemons (daemon_id, player_id, pid, command, started_at, status, log_file, err_file)
            VALUES (?, ?, ?, ?, ?, 'running', ?, ?)
        """, (daemon_id, player_id, pid, json.dumps(command),
              datetime.utcnow().isoformat(), log_file, err_file))

        return True

    def update_daemon_status(self, conn: sqlite3.Connection, player_id: int, daemon_id: str,
                           status: str, stopped_at: Optional[str] = None) -> bool:
        """Update daemon status"""
        cursor = conn.cursor()

        if stopped_at:
            cursor.execute("""
                UPDATE daemons
                SET status = ?, stopped_at = ?
                WHERE daemon_id = ? AND player_id = ?
            """, (status, stopped_at, daemon_id, player_id))
        else:
            cursor.execute("""
                UPDATE daemons
                SET status = ?
                WHERE daemon_id = ? AND player_id = ?
            """, (status, daemon_id, player_id))

        return cursor.rowcount > 0

    def get_daemon(self, conn: sqlite3.Connection, player_id: int, daemon_id: str) -> Optional[Dict]:
        """Get daemon details"""
        cursor = conn.cursor()

        cursor.execute("""
            SELECT * FROM daemons WHERE daemon_id = ? AND player_id = ?
        """, (daemon_id, player_id))

        row = cursor.fetchone()
        if row:
            result = dict(row)
            if result.get('command'):
                result['command'] = json.loads(result['command'])
            return result
        return None

    def list_daemons(self, conn: sqlite3.Connection, player_id: int,
                    status: Optional[str] = None) -> List[Dict]:
        """List all daemons for a player, optionally filtered by status"""
        cursor = conn.cursor()

        if status:
            cursor.execute("""
                SELECT * FROM daemons WHERE player_id = ? AND status = ?
                ORDER BY started_at DESC
            """, (player_id, status))
        else:
            cursor.execute("""
                SELECT * FROM daemons WHERE player_id = ?
                ORDER BY started_at DESC
            """, (player_id,))

        results = []
        for row in cursor.fetchall():
            result = dict(row)
            if result.get('command'):
                result['command'] = json.loads(result['command'])
            results.append(result)

        return results

    def delete_daemon(self, conn: sqlite3.Connection, player_id: int, daemon_id: str) -> bool:
        """Delete daemon record"""
        cursor = conn.cursor()

        cursor.execute("""
            DELETE FROM daemons WHERE daemon_id = ? AND player_id = ?
        """, (daemon_id, player_id))

        return cursor.rowcount > 0

    # =========================================================================
    # SYSTEM GRAPHS
    # =========================================================================

    def save_system_graph(self, conn: sqlite3.Connection, system_symbol: str,
                         graph_data: Dict) -> bool:
        """
        Save system graph (denormalized for performance)

        Also saves normalized waypoint and edge data for querying
        """
        cursor = conn.cursor()
        now = datetime.utcnow().isoformat()

        # Save denormalized graph
        cursor.execute("""
            INSERT INTO system_graphs (system_symbol, graph_data, created_at, updated_at)
            VALUES (?, ?, ?, ?)
            ON CONFLICT(system_symbol) DO UPDATE SET
                graph_data = excluded.graph_data,
                updated_at = excluded.updated_at
        """, (system_symbol, json.dumps(graph_data), now, now))

        # Delete old normalized data
        cursor.execute("DELETE FROM graph_edges WHERE system_symbol = ?", (system_symbol,))
        cursor.execute("DELETE FROM waypoints WHERE system_symbol = ?", (system_symbol,))

        # Save normalized waypoints
        for wp_symbol, wp_data in graph_data.get('waypoints', {}).items():
            cursor.execute("""
                INSERT INTO waypoints
                    (waypoint_symbol, system_symbol, type, x, y, traits, has_fuel, orbitals)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?)
            """, (wp_symbol, system_symbol, wp_data['type'],
                  wp_data['x'], wp_data['y'],
                  json.dumps(wp_data.get('traits', [])),
                  1 if wp_data.get('has_fuel') else 0,
                  json.dumps(wp_data.get('orbitals', []))))

        # Save normalized edges
        for edge in graph_data.get('edges', []):
            cursor.execute("""
                INSERT INTO graph_edges
                    (system_symbol, from_waypoint, to_waypoint, distance, edge_type)
                VALUES (?, ?, ?, ?, ?)
            """, (system_symbol, edge['from'], edge['to'],
                  edge['distance'], edge.get('type', 'normal')))

        return True

    def get_system_graph(self, conn: sqlite3.Connection,
                        system_symbol: str) -> Optional[Dict]:
        """Get system graph (denormalized)"""
        cursor = conn.cursor()

        cursor.execute("""
            SELECT graph_data FROM system_graphs WHERE system_symbol = ?
        """, (system_symbol,))

        row = cursor.fetchone()
        if row:
            return json.loads(row['graph_data'])
        return None

    def list_systems(self, conn: sqlite3.Connection) -> List[str]:
        """List all systems with graphs"""
        cursor = conn.cursor()

        cursor.execute("SELECT system_symbol FROM system_graphs ORDER BY system_symbol")

        return [row['system_symbol'] for row in cursor.fetchall()]

    def find_fuel_stations(self, conn: sqlite3.Connection,
                          system_symbol: str) -> List[str]:
        """Find all waypoints with fuel in a system"""
        cursor = conn.cursor()

        cursor.execute("""
            SELECT waypoint_symbol FROM waypoints
            WHERE system_symbol = ? AND has_fuel = 1
        """, (system_symbol,))

        return [row['waypoint_symbol'] for row in cursor.fetchall()]

    # =========================================================================
    # MARKET DATA
    # =========================================================================

    def update_market_data(self, conn: sqlite3.Connection, waypoint_symbol: str,
                          good_symbol: str, supply: Optional[str], activity: Optional[str],
                          purchase_price: int, sell_price: int,
                          trade_volume: int, last_updated: str, player_id: Optional[int] = None) -> bool:
        """Update market data for a good at a waypoint (SHARED - visible to all players)"""
        cursor = conn.cursor()

        cursor.execute("""
            INSERT INTO market_data
                (waypoint_symbol, good_symbol, supply, activity,
                 purchase_price, sell_price, trade_volume, last_updated, updated_by_player)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT(waypoint_symbol, good_symbol) DO UPDATE SET
                supply = excluded.supply,
                activity = excluded.activity,
                purchase_price = excluded.purchase_price,
                sell_price = excluded.sell_price,
                trade_volume = excluded.trade_volume,
                last_updated = excluded.last_updated,
                updated_by_player = excluded.updated_by_player
        """, (waypoint_symbol, good_symbol, supply, activity,
              purchase_price, sell_price, trade_volume,
              last_updated, player_id))

        return True

    def get_market_data(self, conn: sqlite3.Connection,
                       waypoint_symbol: str,
                       good_symbol: Optional[str] = None) -> List[Dict]:
        """Get market data for a waypoint (SHARED - all players see same data)"""
        cursor = conn.cursor()

        if good_symbol:
            cursor.execute("""
                SELECT * FROM market_data
                WHERE waypoint_symbol = ? AND good_symbol = ?
            """, (waypoint_symbol, good_symbol))
        else:
            cursor.execute("""
                SELECT * FROM market_data WHERE waypoint_symbol = ?
            """, (waypoint_symbol,))

        return [dict(row) for row in cursor.fetchall()]

    def record_transaction(self, conn: sqlite3.Connection, player_id: int, ship_symbol: str,
                          waypoint_symbol: str, good_symbol: str,
                          transaction_type: str, units: int,
                          price_per_unit: int, total_cost: int) -> bool:
        """Record a market transaction for a player"""
        cursor = conn.cursor()

        cursor.execute("""
            INSERT INTO market_transactions
                (player_id, ship_symbol, waypoint_symbol, good_symbol, transaction_type,
                 units, price_per_unit, total_cost, timestamp)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
        """, (player_id, ship_symbol, waypoint_symbol, good_symbol, transaction_type,
              units, price_per_unit, total_cost, datetime.utcnow().isoformat()))

        return True

    def get_transactions(self, conn: sqlite3.Connection, player_id: int,
                        ship_symbol: Optional[str] = None,
                        waypoint_symbol: Optional[str] = None,
                        good_symbol: Optional[str] = None,
                        limit: int = 100) -> List[Dict]:
        """Get transaction history for a player with optional filters"""
        cursor = conn.cursor()

        query = "SELECT * FROM market_transactions WHERE player_id = ?"
        params = [player_id]

        if ship_symbol:
            query += " AND ship_symbol = ?"
            params.append(ship_symbol)

        if waypoint_symbol:
            query += " AND waypoint_symbol = ?"
            params.append(waypoint_symbol)

        if good_symbol:
            query += " AND good_symbol = ?"
            params.append(good_symbol)

        query += " ORDER BY timestamp DESC LIMIT ?"
        params.append(limit)

        cursor.execute(query, params)

        return [dict(row) for row in cursor.fetchall()]

    # =========================================================================
    # TOUR CACHE
    # =========================================================================

    def get_cached_tour(self, conn: sqlite3.Connection, system: str, markets: List[str],
                       algorithm: str, start_waypoint: Optional[str] = None) -> Optional[Dict]:
        """
        Get cached tour result if available

        Args:
            conn: Database connection
            system: System symbol (e.g., 'X1-HU87')
            markets: List of market waypoints (will be sorted for cache key)
            algorithm: Algorithm used ('greedy' or '2opt')
            start_waypoint: Optional starting waypoint

        Returns:
            Cached tour dict with 'tour_order' and 'total_distance', or None if not cached
        """
        cursor = conn.cursor()

        # Sort markets for order-independent cache key
        markets_sorted = sorted(markets)
        markets_key = json.dumps(markets_sorted)

        # Handle NULL start_waypoint in SQL
        if start_waypoint is None:
            cursor.execute("""
                SELECT tour_order, total_distance, calculated_at
                FROM tour_cache
                WHERE system = ? AND markets = ? AND algorithm = ? AND start_waypoint IS NULL
            """, (system, markets_key, algorithm))
        else:
            cursor.execute("""
                SELECT tour_order, total_distance, calculated_at
                FROM tour_cache
                WHERE system = ? AND markets = ? AND algorithm = ? AND start_waypoint = ?
            """, (system, markets_key, algorithm, start_waypoint))

        row = cursor.fetchone()
        if row:
            return {
                'tour_order': json.loads(row['tour_order']),
                'total_distance': row['total_distance'],
                'calculated_at': row['calculated_at']
            }
        return None

    def save_tour_cache(self, conn: sqlite3.Connection, system: str, markets: List[str],
                       algorithm: str, tour_order: List[str], total_distance: float,
                       start_waypoint: Optional[str] = None) -> bool:
        """
        Save tour optimization result to cache

        Args:
            conn: Database connection
            system: System symbol
            markets: List of market waypoints (will be sorted for cache key)
            algorithm: Algorithm used ('greedy' or '2opt')
            tour_order: Ordered list of waypoints in tour
            total_distance: Total tour distance
            start_waypoint: Optional starting waypoint

        Returns:
            True if saved successfully
        """
        cursor = conn.cursor()

        # Sort markets for order-independent cache key
        markets_sorted = sorted(markets)
        markets_key = json.dumps(markets_sorted)
        tour_order_json = json.dumps(tour_order)

        cursor.execute("""
            INSERT INTO tour_cache
                (system, markets, algorithm, start_waypoint, tour_order, total_distance, calculated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT(system, markets, algorithm, start_waypoint) DO UPDATE SET
                tour_order = excluded.tour_order,
                total_distance = excluded.total_distance,
                calculated_at = excluded.calculated_at
        """, (system, markets_key, algorithm, start_waypoint,
              tour_order_json, total_distance, datetime.utcnow().isoformat()))

        return True

    def clear_tour_cache(self, conn: sqlite3.Connection, system: Optional[str] = None) -> int:
        """
        Clear tour cache entries, optionally filtered by system

        Args:
            conn: Database connection
            system: Optional system symbol to filter by

        Returns:
            Number of entries deleted
        """
        cursor = conn.cursor()

        if system:
            cursor.execute("DELETE FROM tour_cache WHERE system = ?", (system,))
        else:
            cursor.execute("DELETE FROM tour_cache")

        return cursor.rowcount


# Singleton instances per path (for test isolation)
_db_instances: Dict[str, Database] = {}


def get_database(db_path: str | Path | None = None) -> Database:
    """Get database singleton instance per path."""
    global _db_instances

    resolved_path = Path(db_path) if db_path else paths.sqlite_path()
    normalized_path = str(resolved_path.resolve())

    if normalized_path not in _db_instances:
        _db_instances[normalized_path] = Database(resolved_path)

    return _db_instances[normalized_path]
