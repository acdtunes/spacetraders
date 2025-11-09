import sqlite3
import logging
import os
from pathlib import Path
from contextlib import contextmanager
from typing import Optional, List, Dict, Any, Union, Tuple
from datetime import datetime, timedelta
import threading

logger = logging.getLogger(__name__)


class ConnectionWrapper:
    """Wrapper for database connections that transparently converts SQL placeholders"""

    def __init__(self, connection, converter_func):
        """
        Wrap a database connection to auto-convert SQL placeholders.

        Args:
            connection: The actual database connection (sqlite3 or psycopg2)
            converter_func: Function to convert SQL (takes str, returns str)
        """
        self._connection = connection
        self._converter = converter_func

    def execute(self, sql: str, parameters=None):
        """Execute SQL with automatic placeholder conversion

        For SQLite: Calls execute() directly on the connection (SQLite supports this)
        For psycopg2: Gets a cursor first, then calls execute (psycopg2 requires this)
        """
        # Check if the underlying connection has execute() method (SQLite)
        # psycopg2 connections don't have execute() - they require cursor()
        if hasattr(self._connection, 'execute'):
            # SQLite path - direct execute on connection (convert here)
            converted_sql = self._converter(sql)
            if parameters is None:
                return self._connection.execute(converted_sql)
            return self._connection.execute(converted_sql, parameters)
        else:
            # psycopg2 path - must use cursor (let CursorWrapper do the conversion)
            cursor = self.cursor()
            if parameters is None:
                cursor.execute(sql)  # Pass original SQL, cursor will convert
            else:
                cursor.execute(sql, parameters)  # Pass original SQL, cursor will convert
            return cursor  # Return the cursor, not the result of execute() (which is None)

    def cursor(self):
        """Get a cursor that auto-converts SQL"""
        return CursorWrapper(self._connection.cursor(), self._converter)

    def commit(self):
        """Commit the transaction"""
        return self._connection.commit()

    def rollback(self):
        """Rollback the transaction"""
        return self._connection.rollback()

    def close(self):
        """Close the connection"""
        return self._connection.close()

    def __getattr__(self, name):
        """Delegate all other attributes to the wrapped connection"""
        return getattr(self._connection, name)


class CursorWrapper:
    """Wrapper for database cursors that transparently converts SQL placeholders"""

    def __init__(self, cursor, converter_func):
        """
        Wrap a database cursor to auto-convert SQL placeholders.

        Args:
            cursor: The actual database cursor
            converter_func: Function to convert SQL (takes str, returns str)
        """
        self._cursor = cursor
        self._converter = converter_func

    def execute(self, sql: str, parameters=None):
        """Execute SQL with automatic placeholder conversion"""
        converted_sql = self._converter(sql)
        if parameters is None:
            return self._cursor.execute(converted_sql)
        return self._cursor.execute(converted_sql, parameters)

    def fetchone(self):
        """Fetch one row"""
        return self._cursor.fetchone()

    def fetchall(self):
        """Fetch all rows"""
        return self._cursor.fetchall()

    def fetchmany(self, size=None):
        """Fetch many rows"""
        if size is None:
            return self._cursor.fetchmany()
        return self._cursor.fetchmany(size)

    @property
    def lastrowid(self):
        """Get last row ID"""
        return self._cursor.lastrowid

    @property
    def rowcount(self):
        """Get row count"""
        return self._cursor.rowcount

    def __getattr__(self, name):
        """Delegate all other attributes to the wrapped cursor"""
        return getattr(self._cursor, name)


class Database:
    """Database manager supporting both SQLite and PostgreSQL backends"""

    def __init__(self, db_path: Optional[Path | str] = None):
        # Check for DATABASE_URL environment variable (PostgreSQL)
        database_url = os.environ.get("DATABASE_URL")

        if database_url and database_url.startswith("postgresql://"):
            # PostgreSQL backend
            self.backend = 'postgresql'
            self.db_url = database_url
            self.db_path = None
            self._persistent_conn = None
            logger.info(f"Using PostgreSQL backend: {database_url}")
        else:
            # SQLite backend
            self.backend = 'sqlite'
            self.db_url = None

            # Handle in-memory database as string, file-based as Path
            if db_path == ":memory:":
                self.db_path = ":memory:"
                # For in-memory databases, keep a persistent connection
                # Otherwise each new connection creates a fresh empty database
                self._persistent_conn = None
            else:
                # Priority: explicit parameter > environment variable > default
                if db_path is not None:
                    self.db_path = Path(db_path) if not isinstance(db_path, Path) else db_path
                else:
                    env_path = os.environ.get("SPACETRADERS_DB_PATH")
                    if env_path:
                        self.db_path = Path(env_path)
                    else:
                        self.db_path = Path("var/spacetraders.db")

                self._persistent_conn = None
                # Create directory for file-based databases
                if isinstance(self.db_path, Path):
                    self.db_path.parent.mkdir(parents=True, exist_ok=True)

            logger.info(f"Using SQLite backend: {self.db_path}")

        # Initialize log deduplication cache
        self._log_dedup_cache: Dict[Tuple[str, str], datetime] = {}
        self._log_dedup_lock = threading.Lock()
        self._log_dedup_window = timedelta(seconds=60)  # 60-second deduplication window
        self._log_dedup_max_size = 10000  # Max cache entries before cleanup

        self._init_database()
        logger.info(f"Database initialized")

    def _get_connection(self) -> Union[sqlite3.Connection, Any]:
        """Get database connection with optimized settings"""
        if self.backend == 'postgresql':
            # PostgreSQL connection
            import psycopg2
            import psycopg2.extras

            conn = psycopg2.connect(self.db_url)
            # Use RealDictCursor for dict-like row access
            conn.cursor_factory = psycopg2.extras.RealDictCursor
            return conn

        # SQLite connection
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
        """Context manager for read-only connections with automatic SQL conversion"""
        conn = self._get_connection()
        wrapped_conn = ConnectionWrapper(conn, self._convert_placeholders)
        try:
            yield wrapped_conn
        finally:
            # Don't close persistent connections for in-memory databases
            if self.backend == 'sqlite' and self.db_path == ":memory:":
                pass  # Keep connection open
            else:
                conn.close()

    @contextmanager
    def transaction(self):
        """Context manager for transactional writes with automatic SQL conversion"""
        conn = self._get_connection()
        wrapped_conn = ConnectionWrapper(conn, self._convert_placeholders)
        try:
            yield wrapped_conn
            conn.commit()
        except Exception:
            conn.rollback()
            raise
        finally:
            # Don't close persistent connections for in-memory databases
            if self.backend == 'sqlite' and self.db_path == ":memory:":
                pass  # Keep connection open
            else:
                conn.close()

    def close(self):
        """Close database connections, particularly the persistent in-memory connection"""
        if self._persistent_conn is not None:
            logger.debug("Closing persistent database connection")
            self._persistent_conn.close()
            self._persistent_conn = None

    def is_closed(self) -> bool:
        """Check if database connection is closed

        Returns:
            True if database is closed, False otherwise
        """
        # For in-memory databases, check if persistent connection is closed
        if self.db_path == ":memory:":
            return self._persistent_conn is None
        # For file-based databases, we don't maintain a persistent connection
        # so we consider them "open" unless explicitly closed
        return False

    def _get_sql_type(self, sql_type: str) -> str:
        """Get backend-specific SQL type"""
        if self.backend == 'postgresql':
            # Map SQLite types to PostgreSQL types
            type_map = {
                'INTEGER PRIMARY KEY AUTOINCREMENT': 'SERIAL PRIMARY KEY',
                'TEXT': 'TEXT',
                'INTEGER': 'INTEGER',
                'REAL': 'REAL',
                'BOOLEAN': 'BOOLEAN',
                'TIMESTAMP': 'TIMESTAMP',
            }
            return type_map.get(sql_type, sql_type)
        return sql_type

    def _get_placeholder(self) -> str:
        """Get parameter placeholder for SQL queries"""
        return '%s' if self.backend == 'postgresql' else '?'

    def _convert_placeholders(self, sql: str) -> str:
        """
        Convert SQLite-style placeholders (?) to backend-specific format.

        For PostgreSQL: ? -> $1, $2, $3, ... (PostgreSQL numbered parameters)
        For SQLite: no conversion needed

        Args:
            sql: SQL query string with ? placeholders

        Returns:
            SQL query string with backend-specific placeholders
        """
        if self.backend != 'postgresql':
            return sql

        # Convert ? to $1, $2, $3, ... for PostgreSQL
        result = []
        param_num = 1
        i = 0
        while i < len(sql):
            if sql[i] == '?':
                result.append(f'${param_num}')
                param_num += 1
            else:
                result.append(sql[i])
            i += 1
        return ''.join(result)

    def _init_database(self):
        """Initialize database schema"""
        # Use transaction for schema initialization
        with self.transaction() as conn:
            cursor = conn.cursor()

            # Players table
            # NOTE: credits are synchronized from SpaceTraders API via SyncPlayerCommand
            if self.backend == 'postgresql':
                cursor.execute("""
                    CREATE TABLE IF NOT EXISTS players (
                        player_id SERIAL PRIMARY KEY,
                        agent_symbol TEXT UNIQUE NOT NULL,
                        token TEXT NOT NULL,
                        created_at TIMESTAMP NOT NULL,
                        last_active TIMESTAMP,
                        metadata TEXT,
                        credits INTEGER DEFAULT 0
                    )
                """)
            else:
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
            if self.backend == 'postgresql':
                # PostgreSQL: Check column existence
                cursor.execute("""
                    SELECT column_name FROM information_schema.columns
                    WHERE table_name = 'players' AND column_name = 'credits'
                """)
                if not cursor.fetchone():
                    logger.info("Adding credits column to players table")
                    cursor.execute("ALTER TABLE players ADD COLUMN credits INTEGER DEFAULT 0")
            else:
                # SQLite: Use PRAGMA
                cursor.execute("PRAGMA table_info(players)")
                player_columns = [row[1] for row in cursor.fetchall()]
                if 'credits' not in player_columns:
                    logger.info("Adding credits column to players table")
                    cursor.execute("ALTER TABLE players ADD COLUMN credits INTEGER DEFAULT 0")

            # System graphs table (shared across all players)
            cursor.execute("""
                CREATE TABLE IF NOT EXISTS system_graphs (
                    system_symbol TEXT PRIMARY KEY,
                    graph_data TEXT NOT NULL,
                    last_updated TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
                )
            """)

            # Ships table removed - ship data is now fetched directly from API
            # Historical note: Ships table was removed to ensure ship state
            # (location, fuel, cargo) is always fresh from the SpaceTraders API.
            # This prevents stale data issues and eliminates sync complexity.

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
                    command_type TEXT,
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

            # Migrate existing containers: add command_type column if missing
            if self.backend == 'sqlite':
                # Check if command_type column exists
                cursor.execute("PRAGMA table_info(containers)")
                columns = [row[1] for row in cursor.fetchall()]
                if 'command_type' not in columns:
                    cursor.execute("ALTER TABLE containers ADD COLUMN command_type TEXT DEFAULT NULL")
                    logger.info("Added command_type column to containers table")
            elif self.backend == 'postgresql':
                # Check if command_type column exists
                cursor.execute("""
                    SELECT column_name FROM information_schema.columns
                    WHERE table_name = 'containers' AND column_name = 'command_type'
                """)
                if not cursor.fetchone():
                    cursor.execute("ALTER TABLE containers ADD COLUMN command_type TEXT DEFAULT NULL")
                    logger.info("Added command_type column to containers table")

            # Backfill command_type from JSON config for existing containers
            if self.backend == 'sqlite':
                cursor.execute("""
                    UPDATE containers
                    SET command_type = json_extract(config, '$.command_type')
                    WHERE command_type IS NULL AND config IS NOT NULL
                """)
                updated_count = cursor.execute("SELECT changes()").fetchone()[0]
                if updated_count > 0:
                    logger.info(f"Backfilled command_type for {updated_count} existing containers")
            elif self.backend == 'postgresql':
                cursor.execute("""
                    UPDATE containers
                    SET command_type = config::json->>'command_type'
                    WHERE command_type IS NULL AND config IS NOT NULL
                """)
                if cursor.rowcount > 0:
                    logger.info(f"Backfilled command_type for {cursor.rowcount} existing containers")

            # Container logs table
            if self.backend == 'postgresql':
                cursor.execute("""
                    CREATE TABLE IF NOT EXISTS container_logs (
                        log_id SERIAL PRIMARY KEY,
                        container_id TEXT NOT NULL,
                        player_id INTEGER NOT NULL,
                        timestamp TIMESTAMP NOT NULL,
                        level TEXT NOT NULL DEFAULT 'INFO',
                        message TEXT NOT NULL,
                        FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
                    )
                """)
            else:
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

            # Migration: Add synced_at column if it doesn't exist
            if self.backend == 'postgresql':
                # PostgreSQL: Check column existence
                cursor.execute("""
                    SELECT column_name FROM information_schema.columns
                    WHERE table_name = 'waypoints' AND column_name = 'synced_at'
                """)
                if not cursor.fetchone():
                    logger.info("Adding synced_at column to waypoints table")
                    cursor.execute("ALTER TABLE waypoints ADD COLUMN synced_at TIMESTAMP DEFAULT NULL")
            else:
                # SQLite: Use PRAGMA
                cursor.execute("PRAGMA table_info(waypoints)")
                waypoint_columns = [row[1] for row in cursor.fetchall()]
                if 'synced_at' not in waypoint_columns:
                    logger.info("Adding synced_at column to waypoints table")
                    # Set default to NULL so we can detect waypoints that have never been synced
                    cursor.execute("ALTER TABLE waypoints ADD COLUMN synced_at TIMESTAMP DEFAULT NULL")

            # Captain logs table (narrative mission logs from TARS AI captain)
            if self.backend == 'postgresql':
                cursor.execute("""
                    CREATE TABLE IF NOT EXISTS captain_logs (
                        log_id SERIAL PRIMARY KEY,
                        player_id INTEGER NOT NULL,
                        timestamp TIMESTAMP NOT NULL,
                        entry_type TEXT NOT NULL,
                        narrative TEXT NOT NULL,
                        event_data TEXT,
                        tags TEXT,
                        fleet_snapshot TEXT,
                        FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
                    )
                """)
            else:
                cursor.execute("""
                    CREATE TABLE IF NOT EXISTS captain_logs (
                        log_id INTEGER PRIMARY KEY AUTOINCREMENT,
                        player_id INTEGER NOT NULL,
                        timestamp TIMESTAMP NOT NULL,
                        entry_type TEXT NOT NULL,
                        narrative TEXT NOT NULL,
                        event_data TEXT,
                        tags TEXT,
                        fleet_snapshot TEXT,
                        FOREIGN KEY (player_id) REFERENCES players(player_id) ON DELETE CASCADE
                    )
                """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_captain_logs_player_time
                ON captain_logs(player_id, timestamp DESC)
            """)

            cursor.execute("""
                CREATE INDEX IF NOT EXISTS idx_captain_logs_entry_type
                ON captain_logs(player_id, entry_type)
            """)

    def log_to_database(self, container_id: str, player_id: int, message: str, level: str = "INFO"):
        """
        Log container message to database with time-windowed deduplication.

        Suppresses duplicate messages within the deduplication window (default 60 seconds)
        to reduce log volume while preserving all unique events.

        Args:
            container_id: Container identifier
            player_id: Player identifier
            message: Log message
            level: Log level (INFO, WARNING, ERROR, DEBUG)
        """
        now = datetime.now()
        cache_key = (container_id, message)

        # Thread-safe deduplication check
        with self._log_dedup_lock:
            # Check if this message was logged recently
            if cache_key in self._log_dedup_cache:
                last_logged = self._log_dedup_cache[cache_key]
                if now - last_logged < self._log_dedup_window:
                    # Duplicate within window, skip logging
                    return

            # Clean up cache if it's getting too large
            if len(self._log_dedup_cache) >= self._log_dedup_max_size:
                self._cleanup_dedup_cache()

            # Update cache with current timestamp
            self._log_dedup_cache[cache_key] = now

        # Log to database (outside lock to minimize lock contention)
        with self.transaction() as conn:
            conn.execute("""
                INSERT INTO container_logs
                (container_id, player_id, timestamp, level, message)
                VALUES (?, ?, ?, ?, ?)
            """, (container_id, player_id, now.isoformat(), level, message))

    def _cleanup_dedup_cache(self):
        """
        Clean up old entries from the deduplication cache.

        Removes entries older than the deduplication window to prevent unbounded
        memory growth. Called automatically when cache size exceeds threshold.

        Note: Must be called while holding self._log_dedup_lock
        """
        now = datetime.now()
        cutoff = now - self._log_dedup_window

        # Remove entries older than the deduplication window
        keys_to_remove = [
            key for key, timestamp in self._log_dedup_cache.items()
            if timestamp < cutoff
        ]

        for key in keys_to_remove:
            del self._log_dedup_cache[key]

        logger.debug(f"Cleaned up {len(keys_to_remove)} old entries from log deduplication cache")

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
                    'timestamp': row['timestamp'].isoformat() if isinstance(row['timestamp'], datetime) else row['timestamp'],
                    'level': row['level'],
                    'message': row['message']
                }
                for row in rows
            ]

    def insert_container(
        self,
        container_id: str,
        player_id: int,
        container_type: str,
        status: str,
        restart_policy: str,
        config: str,
        started_at: str,
        command_type: Optional[str] = None
    ):
        """
        Insert a new container record into the database.

        Args:
            container_id: Unique container identifier
            player_id: Player identifier
            container_type: Type of container (e.g., 'command')
            status: Initial status (e.g., 'STARTING')
            restart_policy: Restart policy ('no', 'on-failure', 'always')
            config: JSON string of container configuration
            started_at: ISO format timestamp when container was started
            command_type: Optional command type (e.g., 'scout_markets', 'navigate', 'purchase_ship')
        """
        with self.transaction() as conn:
            conn.execute("""
                INSERT INTO containers (
                    container_id, player_id, container_type, command_type, status,
                    restart_policy, restart_count, config, started_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
            """, (container_id, player_id, container_type, command_type, status, restart_policy, 0, config, started_at))

    def update_container_status(
        self,
        container_id: str,
        player_id: int,
        status: str,
        stopped_at: Optional[str] = None,
        exit_code: Optional[int] = None,
        exit_reason: Optional[str] = None
    ):
        """
        Update container status in the database.

        Args:
            container_id: Container identifier
            player_id: Player identifier
            status: New status ('RUNNING', 'STOPPED', 'FAILED', etc.)
            stopped_at: Optional ISO format timestamp when container stopped
            exit_code: Optional exit code (0 for success, non-zero for failure)
            exit_reason: Optional reason for failure
        """
        with self.transaction() as conn:
            if stopped_at is not None:
                conn.execute("""
                    UPDATE containers
                    SET status = ?, stopped_at = ?, exit_code = ?, exit_reason = ?
                    WHERE container_id = ? AND player_id = ?
                """, (status, stopped_at, exit_code, exit_reason, container_id, player_id))
            else:
                conn.execute("""
                    UPDATE containers
                    SET status = ?
                    WHERE container_id = ? AND player_id = ?
                """, (status, container_id, player_id))

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
