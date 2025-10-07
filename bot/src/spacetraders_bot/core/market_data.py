"""High-level helpers for querying market data stored in SQLite."""

from __future__ import annotations

import logging
from datetime import datetime, timedelta, timezone
from typing import Dict, Iterable, List, Optional

from ..helpers import paths
from .database import Database, get_database

logger = logging.getLogger(__name__)

SUPPLY_ORDER = [
    "SCARCE",
    "LIMITED",
    "MODERATE",
    "HIGH",
    "ABUNDANT",
]

ACTIVITY_ORDER = [
    "WEAK",
    "FAIR",
    "STRONG",
    "EXCESSIVE",
]


def _resolve_db(db: Optional[Database], db_path: Optional[str]) -> Database:
    if db is not None:
        return db
    resolved = paths.sqlite_path() if db_path is None else db_path
    return get_database(resolved)


def _system_like(system: Optional[str]) -> Optional[str]:
    if not system:
        return None
    return f"{system}%"


def _filter_by_minimum(value: Optional[str], minimum: Optional[str], ordering: Iterable[str]) -> bool:
    if minimum is None:
        return True
    if value is None:
        return False
    try:
        idx_value = ordering.index(value.upper())
    except ValueError:
        return False
    try:
        idx_min = ordering.index(minimum.upper())
    except ValueError:
        logger.warning("Unknown minimum filter '%s'", minimum)
        return True
    return idx_value >= idx_min


def get_waypoint_goods(
    waypoint_symbol: str,
    *,
    db: Optional[Database] = None,
    db_path: Optional[str] = None,
) -> List[Dict]:
    """Return all goods recorded for a waypoint."""
    database = _resolve_db(db, db_path)
    with database.connection() as conn:
        cursor = conn.execute(
            """
            SELECT waypoint_symbol, good_symbol, supply, activity,
                   purchase_price, sell_price, trade_volume, last_updated
            FROM market_data
            WHERE waypoint_symbol = ?
            ORDER BY good_symbol ASC
            """,
            (waypoint_symbol,),
        )
        return [dict(row) for row in cursor.fetchall()]


def get_waypoint_good(
    waypoint_symbol: str,
    good_symbol: str,
    *,
    db: Optional[Database] = None,
    db_path: Optional[str] = None,
) -> Optional[Dict]:
    """Return a specific good entry for a waypoint."""
    database = _resolve_db(db, db_path)
    with database.connection() as conn:
        cursor = conn.execute(
            """
            SELECT waypoint_symbol, good_symbol, supply, activity,
                   purchase_price, sell_price, trade_volume, last_updated
            FROM market_data
            WHERE waypoint_symbol = ? AND good_symbol = ?
            LIMIT 1
            """,
            (waypoint_symbol, good_symbol),
        )
        row = cursor.fetchone()
        return dict(row) if row else None


def find_markets_selling(
    good_symbol: str,
    *,
    system: Optional[str] = None,
    min_supply: Optional[str] = None,
    updated_within_hours: Optional[float] = None,
    limit: int = 10,
    db: Optional[Database] = None,
    db_path: Optional[str] = None,
) -> List[Dict]:
    """Find markets selling a good ordered by ascending purchase price."""
    database = _resolve_db(db, db_path)
    params: List = [good_symbol]
    clauses = ["good_symbol = ?", "purchase_price IS NOT NULL"]

    if system_like := _system_like(system):
        clauses.append("waypoint_symbol LIKE ?")
        params.append(system_like)

    if updated_within_hours is not None:
        cutoff = datetime.now(timezone.utc) - timedelta(hours=updated_within_hours)
        clauses.append("last_updated >= ?")
        params.append(cutoff.isoformat().replace("+00:00", "Z"))

    query = f"""
        SELECT waypoint_symbol, good_symbol, supply, activity,
               purchase_price, sell_price, trade_volume, last_updated
        FROM market_data
        WHERE {' AND '.join(clauses)}
        ORDER BY purchase_price ASC, last_updated DESC
        LIMIT {max(limit if limit is not None else 0, 0)}
    """

    with database.connection() as conn:
        cursor = conn.execute(query, params)
        rows = [dict(row) for row in cursor.fetchall()]

    if min_supply is None:
        return rows

    return [
        row
        for row in rows
        if _filter_by_minimum(row.get("supply"), min_supply, SUPPLY_ORDER)
    ]


def find_markets_buying(
    good_symbol: str,
    *,
    system: Optional[str] = None,
    min_activity: Optional[str] = None,
    updated_within_hours: Optional[float] = None,
    limit: int = 10,
    db: Optional[Database] = None,
    db_path: Optional[str] = None,
) -> List[Dict]:
    """Find markets buying a good ordered by descending sell price."""
    database = _resolve_db(db, db_path)
    params: List = [good_symbol]
    clauses = ["good_symbol = ?", "sell_price IS NOT NULL"]

    if system_like := _system_like(system):
        clauses.append("waypoint_symbol LIKE ?")
        params.append(system_like)

    if updated_within_hours is not None:
        cutoff = datetime.now(timezone.utc) - timedelta(hours=updated_within_hours)
        clauses.append("last_updated >= ?")
        params.append(cutoff.isoformat().replace("+00:00", "Z"))

    query = f"""
        SELECT waypoint_symbol, good_symbol, supply, activity,
               purchase_price, sell_price, trade_volume, last_updated
        FROM market_data
        WHERE {' AND '.join(clauses)}
        ORDER BY sell_price DESC, last_updated DESC
        LIMIT {max(limit if limit is not None else 0, 0)}
    """

    with database.connection() as conn:
        cursor = conn.execute(query, params)
        rows = [dict(row) for row in cursor.fetchall()]

    if min_activity is None:
        return rows

    return [
        row
        for row in rows
        if _filter_by_minimum(row.get("activity"), min_activity, ACTIVITY_ORDER)
    ]


def get_recent_updates(
    *,
    system: Optional[str] = None,
    limit: int = 25,
    db: Optional[Database] = None,
    db_path: Optional[str] = None,
) -> List[Dict]:
    """Return most recent market updates, optionally narrowed to a system."""
    database = _resolve_db(db, db_path)
    params: List = []
    clauses: List[str] = []

    if system_like := _system_like(system):
        clauses.append("waypoint_symbol LIKE ?")
        params.append(system_like)

    where_clause = f"WHERE {' AND '.join(clauses)}" if clauses else ""

    query = f"""
        SELECT waypoint_symbol, good_symbol, supply, activity,
               purchase_price, sell_price, trade_volume, last_updated
        FROM market_data
        {where_clause}
        ORDER BY last_updated DESC
        LIMIT {max(limit if limit is not None else 0, 0)}
    """

    with database.connection() as conn:
        cursor = conn.execute(query, params)
        return [dict(row) for row in cursor.fetchall()]


def get_stale_markets(
    max_age_hours: float,
    *,
    system: Optional[str] = None,
    db: Optional[Database] = None,
    db_path: Optional[str] = None,
) -> List[Dict]:
    """Return market entries older than the provided age threshold."""
    database = _resolve_db(db, db_path)
    cutoff = datetime.now(timezone.utc) - timedelta(hours=max_age_hours)
    params: List = [cutoff.isoformat().replace("+00:00", "Z")]
    clauses = ["(last_updated IS NULL OR last_updated < ?)"]

    if system_like := _system_like(system):
        clauses.append("waypoint_symbol LIKE ?")
        params.append(system_like)

    query = f"""
        SELECT waypoint_symbol, good_symbol, supply, activity,
               purchase_price, sell_price, trade_volume, last_updated
        FROM market_data
        WHERE {' AND '.join(clauses)}
        ORDER BY (last_updated IS NOT NULL), last_updated ASC
    """

    with database.connection() as conn:
        cursor = conn.execute(query, params)
        return [dict(row) for row in cursor.fetchall()]


def summarize_good(
    good_symbol: str,
    *,
    system: Optional[str] = None,
    db: Optional[Database] = None,
    db_path: Optional[str] = None,
) -> Optional[Dict]:
    """Return aggregate statistics for a trade good."""
    database = _resolve_db(db, db_path)
    params: List = [good_symbol]
    clauses = ["good_symbol = ?"]

    if system_like := _system_like(system):
        clauses.append("waypoint_symbol LIKE ?")
        params.append(system_like)

    query = f"""
        SELECT
            COUNT(*) AS market_count,
            MIN(purchase_price) AS min_purchase_price,
            AVG(purchase_price) AS avg_purchase_price,
            MAX(purchase_price) AS max_purchase_price,
            MIN(sell_price) AS min_sell_price,
            AVG(sell_price) AS avg_sell_price,
            MAX(sell_price) AS max_sell_price,
            MAX(last_updated) AS last_updated
        FROM market_data
        WHERE {' AND '.join(clauses)}
    """

    with database.connection() as conn:
        cursor = conn.execute(query, params)
        row = cursor.fetchone()

    if not row or row["market_count"] == 0:
        return None

    summary = dict(row)

    # SQLite AVG returns float; round to int if perfectly integral
    for key in ("avg_purchase_price", "avg_sell_price"):
        value = summary.get(key)
        if value is None:
            continue
        if abs(value - round(value)) < 1e-9:
            summary[key] = int(round(value))
    return summary
