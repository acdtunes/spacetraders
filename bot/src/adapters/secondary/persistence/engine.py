"""SQLAlchemy engine factory for database connections.

This module provides the create_engine_from_config() function that creates
database engines based on environment configuration, supporting:
- PostgreSQL (via DATABASE_URL environment variable)
- SQLite file-based (via SPACETRADERS_DB_PATH or default var/spacetraders.db)
- SQLite in-memory (for testing, db_path=":memory:")
"""

import os
import json
from pathlib import Path
from typing import Optional
from sqlalchemy import create_engine, Engine, event
from sqlalchemy.pool import StaticPool
import logging

logger = logging.getLogger(__name__)


def create_engine_from_config(db_path: Optional[str] = None) -> Engine:
    """
    Create SQLAlchemy engine from configuration.

    Automatically selects backend based on environment:
    - DATABASE_URL set → PostgreSQL
    - db_path=":memory:" → SQLite in-memory (for tests)
    - Otherwise → SQLite file-based

    Args:
        db_path: Optional explicit database path.
                 Use ":memory:" for in-memory SQLite (testing).
                 None uses environment or default.

    Returns:
        Configured SQLAlchemy Engine instance

    Examples:
        # Production PostgreSQL (from DATABASE_URL env var)
        engine = create_engine_from_config()

        # Testing with in-memory SQLite
        engine = create_engine_from_config(":memory:")

        # File-based SQLite with custom path
        engine = create_engine_from_config("var/test.db")
    """
    database_url = os.environ.get("DATABASE_URL")

    if database_url and database_url.startswith("postgresql://"):
        # PostgreSQL backend
        logger.info(f"Creating PostgreSQL engine: {database_url.split('@')[1]}")  # Hide credentials

        engine = create_engine(
            database_url,
            pool_size=5,
            max_overflow=10,
            pool_pre_ping=True,  # Verify connections before use
            json_serializer=json.dumps,
            json_deserializer=json.loads,
            echo=False,  # Set to True for SQL query logging
        )

        return engine

    else:
        # SQLite backend
        if db_path == ":memory:":
            # In-memory database for testing
            # CRITICAL: Use StaticPool to maintain single connection
            # Otherwise each connection creates a fresh empty database
            logger.info("Creating SQLite in-memory engine (testing mode)")

            engine = create_engine(
                "sqlite:///:memory:",
                connect_args={'check_same_thread': False},
                poolclass=StaticPool,  # Single persistent connection
                json_serializer=json.dumps,
                json_deserializer=json.loads,
                echo=False,
            )

            # Enable foreign keys for in-memory database
            @event.listens_for(engine, "connect")
            def set_sqlite_pragma(dbapi_conn, connection_record):
                cursor = dbapi_conn.cursor()
                cursor.execute("PRAGMA foreign_keys=ON")
                cursor.close()

            return engine

        else:
            # File-based SQLite
            # Priority: explicit parameter > environment variable > default
            if db_path is not None:
                sqlite_path = Path(db_path) if not isinstance(db_path, Path) else db_path
            else:
                env_path = os.environ.get("SPACETRADERS_DB_PATH")
                if env_path and env_path != ":memory:":
                    sqlite_path = Path(env_path)
                else:
                    sqlite_path = Path("var/spacetraders.db")

            # Create directory if it doesn't exist
            sqlite_path.parent.mkdir(parents=True, exist_ok=True)

            logger.info(f"Creating SQLite file engine: {sqlite_path}")

            engine = create_engine(
                f"sqlite:///{sqlite_path}",
                connect_args={'check_same_thread': False},
                json_serializer=json.dumps,
                json_deserializer=json.loads,
                echo=False,
            )

            # Enable foreign keys and WAL mode for file-based database
            @event.listens_for(engine, "connect")
            def set_sqlite_pragma(dbapi_conn, connection_record):
                cursor = dbapi_conn.cursor()
                cursor.execute("PRAGMA foreign_keys=ON")
                cursor.execute("PRAGMA journal_mode=WAL")
                cursor.close()

            return engine


def get_database_url() -> str:
    """
    Get the database URL that would be used by create_engine_from_config().

    Useful for Alembic configuration and debugging.

    Returns:
        Database URL string
    """
    database_url = os.environ.get("DATABASE_URL")

    if database_url and database_url.startswith("postgresql://"):
        return database_url
    else:
        env_path = os.environ.get("SPACETRADERS_DB_PATH")
        if env_path == ":memory:":
            return "sqlite:///:memory:"
        elif env_path:
            return f"sqlite:///{env_path}"
        else:
            return "sqlite:///var/spacetraders.db"
