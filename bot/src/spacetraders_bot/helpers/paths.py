"""Path utilities for the SpaceTraders bot package."""

from __future__ import annotations

from pathlib import Path
from typing import Iterable


PACKAGE_ROOT = Path(__file__).resolve().parents[1]
BOT_ROOT = PACKAGE_ROOT.parents[1]
CONFIG_DIR = BOT_ROOT / "config"
VAR_DIR = BOT_ROOT / "var"
DATA_DIR = VAR_DIR / "data"
SQLITE_DIR = DATA_DIR / "sqlite"
LOGS_DIR = VAR_DIR / "logs"
STATE_DIR = VAR_DIR / "state"
DAEMON_DIR = VAR_DIR / "daemons"
DAEMON_LOGS_DIR = DAEMON_DIR / "logs"
DAEMON_PIDS_DIR = DAEMON_DIR / "pids"
AGENT_CONFIG_DIR = CONFIG_DIR / "agents"

DEFAULT_DATABASE_NAME = "spacetraders.db"


def sqlite_path(name: str = DEFAULT_DATABASE_NAME) -> Path:
    """Return the path to a SQLite database stored under ``var/data/sqlite``."""
    return SQLITE_DIR / name


def captain_logs_root(agent_symbol: str) -> Path:
    """Return the root directory for captain logs for *agent_symbol*."""
    agent_dir = LOGS_DIR / "captain" / agent_symbol.lower()
    ensure_dirs(
        (
            agent_dir,
            agent_dir / "sessions",
            agent_dir / "executive_reports",
        )
    )
    return agent_dir


def ensure_dirs(directories: Iterable[Path]) -> None:
    """Ensure each directory in *directories* exists."""
    for directory in directories:
        directory.mkdir(parents=True, exist_ok=True)


# Ensure default directory structure is present during import in developer environments.
ensure_dirs(
    (
        CONFIG_DIR,
        AGENT_CONFIG_DIR,
        VAR_DIR,
        DATA_DIR,
        SQLITE_DIR,
        LOGS_DIR,
        STATE_DIR,
        DAEMON_DIR,
        DAEMON_LOGS_DIR,
        DAEMON_PIDS_DIR,
    )
)
