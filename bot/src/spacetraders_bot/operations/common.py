#!/usr/bin/env python3
"""
Common utilities for SpaceTraders bot operations
"""

import logging
import sys
from datetime import datetime, timedelta
from functools import lru_cache
from typing import Optional

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.core.database import get_database
from spacetraders_bot.core.utils import timestamp_iso
from spacetraders_bot.helpers.paths import LOGS_DIR, ensure_dirs, sqlite_path
from spacetraders_bot.operations.captain_logging import CaptainLogWriter


logger = logging.getLogger(__name__)


CAPTAIN_DEFAULT_OPERATOR = "AI First Mate"


def get_api_client(player_id: int, db_path: Optional[str] = None):
    """
    Get API client for a player using their stored token

    Args:
        player_id: Player ID from database
        db_path: Path to SQLite database (default: data/spacetraders.db)

    Returns:
        APIClient instance configured with player's token

    Raises:
        ValueError: If player not found
    """
    db_location = db_path or str(sqlite_path())
    db = get_database(db_location)
    with db.connection() as conn:
        player = db.get_player_by_id(conn, player_id)
        if not player:
            raise ValueError(f"Player ID {player_id} not found in database")
        return APIClient(token=player['token'])


def format_credits(amount: int) -> str:
    """Format credits with commas"""
    return f"{amount:,}"


def setup_logging(operation: str, ship: str = "system", log_level: str = "INFO"):
    """
    Setup structured logging for the bot

    Configures both console and file logging with appropriate levels.

    Args:
        operation: Operation name (mining, scout, etc.)
        ship: Ship symbol or "system"
        log_level: Logging level (INFO, WARNING, ERROR)

    Returns:
        Path to log file
    """
    ensure_dirs((LOGS_DIR,))
    log_file = LOGS_DIR / f"{operation}_{ship}_{timestamp_iso().replace(':', '-')}.log"

    # Clear any existing handlers
    root_logger = logging.getLogger()
    root_logger.handlers.clear()

    # Set root logger level to INFO
    root_logger.setLevel(logging.INFO)

    # Create formatters
    detailed_formatter = logging.Formatter(
        '[%(asctime)s] [%(levelname)8s] [%(name)s] %(message)s',
        datefmt='%Y-%m-%d %H:%M:%S'
    )

    console_formatter = logging.Formatter(
        '[%(levelname)s] %(message)s'
    )

    # File handler - captures INFO and above (unbuffered)
    file_handler = logging.FileHandler(log_file, mode='w', encoding='utf-8')
    file_handler.setLevel(logging.INFO)
    file_handler.setFormatter(detailed_formatter)
    # Force unbuffered writes
    file_handler.stream = open(log_file, 'w', encoding='utf-8', buffering=1)
    root_logger.addHandler(file_handler)

    # Console handler - captures INFO and above (or specified level, unbuffered)
    console_handler = logging.StreamHandler(sys.stdout)
    console_level = getattr(logging, log_level.upper(), logging.INFO)
    console_handler.setLevel(console_level)
    console_handler.setFormatter(console_formatter)
    # Force unbuffered console output
    sys.stdout.reconfigure(line_buffering=True) if hasattr(sys.stdout, 'reconfigure') else None
    root_logger.addHandler(console_handler)

    # Log startup
    logging.info("=" * 70)
    logging.info(f"SpaceTraders Bot - {operation.upper()} Operation")
    logging.info(f"Log file: {log_file}")
    logging.info("=" * 70)

    return log_file


@lru_cache(maxsize=8)
def _cached_captain_logger(agent_symbol: str, token: Optional[str]) -> CaptainLogWriter:
    """Cache CaptainLogWriter instances per agent/token pair."""
    return CaptainLogWriter(agent_symbol, token)


def get_captain_logger(player_id: int, db_path: Optional[str] = None) -> Optional[CaptainLogWriter]:
    """Return CaptainLogWriter for the given player, if available."""
    try:
        db_location = db_path or str(sqlite_path())
        db = get_database(db_location)
        with db.connection() as conn:
            player = db.get_player_by_id(conn, player_id)
        if not player:
            logger.warning("Captain log requested for unknown player_id=%s", player_id)
            return None

        agent_symbol = player.get('agent_symbol')
        token = player.get('token')
        if not agent_symbol:
            logger.warning("Player %s missing agent_symbol; cannot initialize captain log", player_id)
            return None

        return _cached_captain_logger(agent_symbol, token)
    except Exception as exc:  # pragma: no cover - defensive logging
        logger.error("Failed to initialize captain log writer: %s", exc)
        return None


def log_captain_event(writer: Optional[CaptainLogWriter], entry_type: str, **kwargs) -> None:
    """Safely emit a captain log entry if writer is available."""
    if writer is None:
        return
    try:
        writer.log_entry(entry_type, **kwargs)
    except Exception as exc:  # pragma: no cover - defensive logging
        logger.error("Captain log entry failed (%s): %s", entry_type, exc)


def get_operator_name(args) -> str:
    """Resolve operator name from args or fall back to default."""
    return getattr(args, 'operator', CAPTAIN_DEFAULT_OPERATOR)


def humanize_duration(delta: timedelta) -> str:
    """Convert timedelta into compact human-readable string."""
    total_seconds = int(delta.total_seconds())
    hours = total_seconds // 3600
    minutes = (total_seconds % 3600) // 60
    seconds = total_seconds % 60

    if hours:
        return f"{hours}h {minutes}m"
    if minutes:
        return f"{minutes}m {seconds}s"
    return f"{seconds}s"
