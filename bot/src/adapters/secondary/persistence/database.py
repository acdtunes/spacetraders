import sqlite3
import logging
from pathlib import Path
from contextlib import contextmanager
from typing import Optional, List, Dict, Any
from datetime import datetime

logger = logging.getLogger(__name__)

class Database:
    """SQLite database manager with WAL mode for concurrency"""

    def __init__(self, db_path: Optional[Path | str] = None):
        # Handle in-memory database as string, file-based as Path
        if db_path == ":memory:":
            self.db_path = ":memory:"
            # For in-memory databases, keep a persistent connection
            # Otherwise each new connection creates a fresh empty database
            self._persistent_conn = None
        else:
            self.db_path = db_path or Path("var/spacetraders.db")
            self._persistent_conn = None
            # Create directory for file-based databases
            if isinstance(self.db_path, Path):
                self.db_path.parent.mkdir(parents=True, exist_ok=True)

        self._init_database()
        logger.info(f"Database initialized at {self.db_path}")

    def _get_connection(self) -> sqlite3.Connection:
        """Get database connection with optimized settings"""
        # For in-memory databases, reuse the persistent connection
        # Otherwise each new connection creates a fresh empty database
        if self.db_path == ":memory:":
            if self._persistent_conn is None:
                self._persistent_conn = sqlite3.connect(
                    ":memory:",
                    check_same_thread=False
                )
                self._persistent_conn.execute('PRAGMA foreign_keys=ON')
                self._persistent_conn.row_factory = sqlite3.Row
            return self._persistent_conn

        # For file-based databases, create new connections as needed
        conn = sqlite3.connect(
            str(self.db_path),
            check_same_thread=False,
            timeout=30.0
        )
        conn.execute('PRAGMA journal_mode=WAL')
        conn.execute('PRAGMA foreign_keys=ON')
        conn.row_factory = sqlite3.Row
        return conn

    @contextmanager
    def connection(self):
        """Context manager for read-only connections"""
        conn = self._get_connection()
        try:
            yield conn
        finally:
            # Don't close persistent connections for in-memory databases
            if self.db_path != ":memory:":
                conn.close()

    @contextmanager
    def transaction(self):
        """Context manager for transactional writes"""
        conn = self._get_connection()
        try:
            yield conn
            conn.commit()
        except Exception:
            conn.rollback()
            raise
        finally:
            # Don't close persistent connections for in-memory databases
            if self.db_path != ":memory:":
                conn.close()

    def close(self):
        """Close database connections, particularly the persistent in-memory connection"""
        if self._persistent_conn is not None:
            logger.debug("Closing persistent database connection")
            self._persistent_conn.close()
            self._persistent_conn = None

    def _init_database(self):
        """Initialize database schema"""
        with self._get_connection() as conn:
            cursor = conn.cursor()

            # Players table
            # NOTE: credits are synchronized from SpaceTraders API via SyncPlayerCommand
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS players (
                    player_id INTEGER PRIMARY KEY AUTOINCREMENT,
                    agent_symbol TEXT UNIQUE NOT NULL,
                    token TEXT NOT NULL,
                    created_at TIMESTAMP NOT NULL,
                    last_active TIMESTAMP,
                    metadata TEXT,
                    credits INTEGER DEFAULT 0
                )
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_player_agent
                ON players(agent_symbol)
            """)

            # Migration: Add credits column if it doesn't exist
            cursor.execute("PRAGMA table_info(players)")
            player_columns = [row[1] for row in cursor.fetchall()]
            if 'credits' not in player_columns:
                logger.info("Adding credits column to players table")
                cursor.execute("ALTER TABLE players ADD COLUMN credits INTEGER DEFAULT 0")
                conn.commit()

            # System graphs table (shared across all players)
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS system_graphs (
                    system_symbol TEXT PRIMARY KEY,
                    graph_data TEXT NOT NULL,
                    last_updated TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
                )
            """)

            # Ships table
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS ships (
                    ship_symbol TEXT NOT NULL,
                    player_id INTEGER NOT NULL,
                    current_location_symbol TEXT NOT NULL,
                    fuel_current INTEGER NOT NULL,
                    fuel_capacity INTEGER NOT NULL,
                    cargo_capacity INTEGER NOT NULL,
                    cargo_units INTEGER NOT NULL,
                    engine_speed INTEGER NOT NULL,
                    nav_status TEXT NOT NULL,
                    system_symbol TEXT NOT NULL,
                    synced_at TIMESTAMP,
                    PRIMARY KEY (ship_symbol, player_id),
                    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
                )
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_ships_player
                ON ships(player_id)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_ships_location
                ON ships(current_location_symbol)
            """)

            # Add synced_at column to existing ships tables (migration)
            # Check if column exists before adding
            cursor.execute("PRAGMA table_info(ships)")
            columns = [row[1] for row in cursor.fetchall()]
            if 'synced_at' not in columns:
                cursor.execute("ALTER TABLE ships ADD COLUMN synced_at TIMESTAMP")
                logger.info("Added synced_at column to ships table")

            # Routes table
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS routes (
                    route_id TEXT PRIMARY KEY,
                    ship_symbol TEXT NOT NULL,
                    player_id INTEGER NOT NULL,
                    status TEXT NOT NULL,
                    current_segment_index INTEGER NOT NULL,
                    ship_fuel_capacity INTEGER NOT NULL,
                    segments_json TEXT NOT NULL,
                    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
                    FOREIGN KEY (ship_symbol, player_id) REFERENCES ships(ship_symbol, player_id) ON DELETE CASCADE
                )
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_routes_ship
                ON routes(ship_symbol, player_id)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_routes_status
                ON routes(status)
            """)

            # Ship assignments table (for container system)
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS ship_assignments (
                    ship_symbol TEXT NOT NULL,
                    player_id INTEGER NOT NULL,
                    container_id TEXT,
                    operation TEXT,
                    status TEXT DEFAULT 'idle',
                    assigned_at TIMESTAMP,
                    released_at TIMESTAMP,
                    release_reason TEXT,
                    PRIMARY KEY (ship_symbol, player_id),
                    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
                )
            """)

            # Containers table (for daemon system)
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS containers (
                    container_id TEXT NOT NULL,
                    player_id INTEGER NOT NULL,
                    container_type TEXT,
                    status TEXT,
                    restart_policy TEXT,
                    restart_count INTEGER DEFAULT 0,
                    config TEXT,
                    started_at TIMESTAMP,
                    stopped_at TIMESTAMP,
                    exit_code INTEGER,
                    exit_reason TEXT,
                    PRIMARY KEY (container_id, player_id),
                    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
                )
            """)

            # Container logs table
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS container_logs (
                    log_id INTEGER PRIMARY KEY AUTOINCREMENT,
                    container_id TEXT NOT NULL,
                    player_id INTEGER NOT NULL,
                    timestamp TIMESTAMP NOT NULL,
                    level TEXT NOT NULL DEFAULT 'INFO',
                    message TEXT NOT NULL,
                    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
                )
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_container_logs_container_time
                ON container_logs(container_id, timestamp DESC)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_container_logs_timestamp
                ON container_logs(timestamp DESC)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_container_logs_level
                ON container_logs(level, timestamp DESC)
            """)

            # Market data table
            # CRITICAL: API field mapping is counter-intuitive!
            # - API purchasePrice = ship PAYS to BUY from market → DB sell_price (market asks high)
            # - API sellPrice = ship RECEIVES when SELLING to market → DB purchase_price (market bids low)
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS market_data (
                    waypoint_symbol TEXT NOT NULL,
                    good_symbol TEXT NOT NULL,
                    supply TEXT,
                    activity TEXT,
                    purchase_price INTEGER NOT NULL,
                    sell_price INTEGER NOT NULL,
                    trade_volume INTEGER NOT NULL,
                    last_updated TIMESTAMP NOT NULL,
                    player_id INTEGER NOT NULL,
                    PRIMARY KEY (waypoint_symbol, good_symbol),
                    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
                )
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_market_data_waypoint
                ON market_data(waypoint_symbol)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_market_data_updated
                ON market_data(last_updated DESC)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_market_data_player
                ON market_data(player_id)
            """)

            # Contracts table
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS contracts (
                    contract_id TEXT NOT NULL,
                    player_id INTEGER NOT NULL,
                    faction_symbol TEXT NOT NULL,
                    type TEXT NOT NULL,
                    accepted BOOLEAN NOT NULL,
                    fulfilled BOOLEAN NOT NULL,
                    deadline_to_accept TIMESTAMP NOT NULL,
                    deadline TIMESTAMP NOT NULL,
                    payment_on_accepted INTEGER NOT NULL,
                    payment_on_fulfilled INTEGER NOT NULL,
                    deliveries_json TEXT NOT NULL,
                    last_updated TIMESTAMP NOT NULL,
                    PRIMARY KEY (contract_id, player_id),
                    FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
                )
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_contracts_player
                ON contracts(player_id)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_contracts_active
                ON contracts(player_id, accepted, fulfilled)
            """)

            # Waypoints table (cached waypoint data for shipyard/market discovery)
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

            conn.commit()

    def log_to_database(self, container_id: str, player_id: int, message: str, level: str = "INFO"):
        """
        Log container message to database.

        Args:
            container_id: Container identifier
            player_id: Player identifier
            message: Log message
            level: Log level (INFO, WARNING, ERROR, DEBUG)
        """
        with self.transaction() as conn:
            conn.execute("""
                INSERT INTO container_logs
                (container_id, player_id, timestamp, level, message)
                VALUES (?, ?, ?, ?, ?)
            """, (container_id, player_id, datetime.now().isoformat(), level, message))

    def get_container_logs(
        self,
        container_id: str,
        player_id: int,
        limit: int = 100,
        level: Optional[str] = None,
        since: Optional[str] = None
    ) -> List[Dict[str, Any]]:
        """
        Get container logs from database.

        Args:
            container_id: Container identifier
            player_id: Player identifier
            limit: Maximum number of logs to return (default 100)
            level: Filter by log level (optional)
            since: Filter by timestamp - only logs after this timestamp (optional)

        Returns:
            List of log dictionaries with keys: log_id, container_id, player_id,
            timestamp, level, message
        """
        with self.connection() as conn:
            query = """
                SELECT log_id, container_id, player_id, timestamp, level, message
                FROM container_logs
                WHERE container_id = ? AND player_id = ?
            """
            params = [container_id, player_id]

            if level:
                query += " AND level = ?"
                params.append(level)

            if since:
                query += " AND timestamp > ?"
                params.append(since)

            query += " ORDER BY timestamp DESC LIMIT ?"
            params.append(limit)

            cursor = conn.execute(query, params)
            rows = cursor.fetchall()

            return [
                {
                    'log_id': row['log_id'],
                    'container_id': row['container_id'],
                    'player_id': row['player_id'],
                    'timestamp': row['timestamp'],
                    'level': row['level'],
                    'message': row['message']
                }
                for row in rows
            ]

    def update_market_data(
        self,
        conn,
        waypoint_symbol: str,
        good_symbol: str,
        supply: Optional[str],
        activity: Optional[str],
        purchase_price: int,
        sell_price: int,
        trade_volume: int,
        last_updated: str,
        player_id: int
    ) -> None:
        """
        Upsert market data for a single trade good.

        CRITICAL - API Field Mapping:
        - purchase_price = what ship RECEIVES when selling to market (API sellPrice)
        - sell_price = what ship PAYS when buying from market (API purchasePrice)

        Args:
            conn: Database connection (use within transaction context)
            waypoint_symbol: Waypoint identifier
            good_symbol: Trade good symbol
            supply: Supply level (SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT)
            activity: Activity level (WEAK, GROWING, STRONG, RESTRICTED)
            purchase_price: What ship receives when selling (API sellPrice)
            sell_price: What ship pays when buying (API purchasePrice)
            trade_volume: Trading volume
            last_updated: ISO timestamp
            player_id: Player ID
        """
        conn.execute("""
            INSERT INTO market_data
            (waypoint_symbol, good_symbol, supply, activity, purchase_price,
             sell_price, trade_volume, last_updated, player_id)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT(waypoint_symbol, good_symbol)
            DO UPDATE SET
                supply = excluded.supply,
                activity = excluded.activity,
                purchase_price = excluded.purchase_price,
                sell_price = excluded.sell_price,
                trade_volume = excluded.trade_volume,
                last_updated = excluded.last_updated,
                player_id = excluded.player_id
        """, (waypoint_symbol, good_symbol, supply, activity, purchase_price,
              sell_price, trade_volume, last_updated, player_id))

    def get_market_data(self, player_id: int, waypoint_symbol: str) -> List[Dict[str, Any]]:
        """
        Get all trade goods for a waypoint.

        Args:
            player_id: Player ID
            waypoint_symbol: Waypoint identifier

        Returns:
            List of trade good dictionaries
        """
        with self.connection() as conn:
            cursor = conn.execute("""
                SELECT waypoint_symbol, good_symbol, supply, activity,
                       purchase_price, sell_price, trade_volume, last_updated
                FROM market_data
                WHERE player_id = ? AND waypoint_symbol = ?
                ORDER BY good_symbol
            """, (player_id, waypoint_symbol))

            rows = cursor.fetchall()
            return [
                {
                    'waypoint_symbol': row['waypoint_symbol'],
                    'good_symbol': row['good_symbol'],
                    'supply': row['supply'],
                    'activity': row['activity'],
                    'purchase_price': row['purchase_price'],
                    'sell_price': row['sell_price'],
                    'trade_volume': row['trade_volume'],
                    'last_updated': row['last_updated']
                }
                for row in rows
            ]

    def list_markets_in_system(
        self,
        player_id: int,
        system_symbol: str,
        max_age_minutes: Optional[int] = None
    ) -> List[str]:
        """
        Get list of waypoints with market data in a system.

        Args:
            player_id: Player ID
            system_symbol: System identifier (e.g., "X1-GZ7")
            max_age_minutes: Optional maximum age in minutes for data freshness

        Returns:
            List of waypoint symbols
        """
        with self.connection() as conn:
            if max_age_minutes:
                # Calculate cutoff time in Python for more reliable comparison
                from datetime import datetime, timedelta, timezone
                cutoff = datetime.now(timezone.utc) - timedelta(minutes=max_age_minutes)
                cutoff_str = cutoff.isoformat()

                query = """
                    SELECT DISTINCT waypoint_symbol, MAX(last_updated) as latest_update
                    FROM market_data
                    WHERE player_id = ? AND waypoint_symbol LIKE ? AND last_updated >= ?
                    GROUP BY waypoint_symbol
                    ORDER BY waypoint_symbol
                """
                params = [player_id, f"{system_symbol}-%", cutoff_str]
            else:
                query = """
                    SELECT DISTINCT waypoint_symbol, MAX(last_updated) as latest_update
                    FROM market_data
                    WHERE player_id = ? AND waypoint_symbol LIKE ?
                    GROUP BY waypoint_symbol
                    ORDER BY waypoint_symbol
                """
                params = [player_id, f"{system_symbol}-%"]

            cursor = conn.execute(query, params)
            rows = cursor.fetchall()
            return [row['waypoint_symbol'] for row in rows]

    def find_cheapest_market_selling(
        self,
        good_symbol: str,
        system: str,
        player_id: int
    ) -> Optional[Dict[str, Any]]:
        """
        Find the cheapest market selling a specific good in a system.

        Args:
            good_symbol: Trade good symbol (e.g., "IRON_ORE")
            system: System symbol (e.g., "X1-TEST")
            player_id: Player ID

        Returns:
            Dictionary with waypoint_symbol, good_symbol, sell_price, supply
            or None if not found
        """
        with self.connection() as conn:
            cursor = conn.execute("""
                SELECT waypoint_symbol, good_symbol, sell_price, supply
                FROM market_data
                WHERE player_id = ?
                  AND good_symbol = ?
                  AND waypoint_symbol LIKE ?
                ORDER BY sell_price ASC
                LIMIT 1
            """, (player_id, good_symbol, f"{system}-%"))

            row = cursor.fetchone()
            if not row:
                return None

            return {
                'waypoint_symbol': row['waypoint_symbol'],
                'good_symbol': row['good_symbol'],
                'sell_price': row['sell_price'],
                'supply': row['supply']
            }
