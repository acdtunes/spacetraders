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


# ============================================================================
# PRICE IMPACT MODEL - Market Dynamics & Batch Trading
# ============================================================================

# Supply multipliers: How quickly prices escalate when buying
# SCARCE supply = high price sensitivity (few units available)
# ABUNDANT supply = low price sensitivity (many units available)
SUPPLY_MULTIPLIERS = {
    "SCARCE": 2.0,     # Prices rise 2x faster
    "LIMITED": 1.5,    # Prices rise 1.5x faster
    "MODERATE": 1.0,   # Baseline price escalation
    "HIGH": 0.5,       # Prices rise 0.5x slower
    "ABUNDANT": 0.3,   # Prices rise 0.3x slower (minimal impact)
}

# Activity multipliers: How quickly prices degrade when selling
# CALIBRATED TO REAL-WORLD DATA (2025-10-12 STARHOPPER-1 execution):
# - SHIP_PLATING (18u/6tv=3x, WEAK): -2.9% actual (old model: -33%)
# - ASSAULT_RIFLES (21u/10tv=2.1x, WEAK): -0.5% actual
# - ADVANCED_CIRCUITRY (20u/20tv=1x, RESTRICTED): -1.9% actual
# Pattern: ~1% degradation per tradeVolume multiple, not exponential
ACTIVITY_MULTIPLIERS = {
    "RESTRICTED": 1.5,  # Reduced from 3.0 (minimal market, but not catastrophic)
    "WEAK": 1.0,        # Reduced from 2.0 (baseline degradation)
    "GROWING": 0.7,     # Reduced from 1.5 (moderate liquidity)
    "STRONG": 0.5,      # Reduced from 1.0 (good liquidity)
    "EXCESSIVE": 0.3,   # Reduced from 0.5 (very high demand, minimal impact)
}


def calculate_batch_purchase_cost(
    base_price: int,
    units: int,
    trade_volume: int,
    supply: Optional[str] = None
) -> tuple[int, Dict]:
    """
    Calculate realistic total cost when buying in batches, accounting for price escalation.

    When buying large quantities, each transaction increases demand, causing prices to rise.
    This models the price impact based on:
    - tradeVolume: Market liquidity depth (larger = more stable prices)
    - supply: Supply level (SCARCE/LIMITED/MODERATE/HIGH/ABUNDANT)

    Based on real-world data:
    - X1-TX46-D42: 18 units SHIP_PLATING (tradeVolume=6, LIMITED)
      - Batch 1: 3,941 cr → Batch 3: 4,580 cr (+16.2%)

    Args:
        base_price: Market's current sell price (what we pay to buy)
        units: Total units to purchase
        trade_volume: Market's tradeVolume (batch size before price impact)
        supply: Market's supply level (affects price sensitivity)

    Returns:
        tuple: (total_cost, breakdown_dict)
        - total_cost: Realistic total credits needed
        - breakdown: Batch-by-batch pricing details with escalation %
    """
    if units <= 0 or base_price <= 0:
        return 0, {'batches': [], 'price_escalation_pct': 0, 'avg_price_per_unit': 0}

    supply_multiplier = SUPPLY_MULTIPLIERS.get(supply or "MODERATE", 1.0)

    # Calculate price escalation per BATCH (not per unit)
    # Real data shows ~7% escalation per batch of 6 units for LIMITED supply
    # Formula calibrated to real-world observations:
    # - Each batch increases price by (base_price * escalation_rate * multiplier)
    # - escalation_rate ≈ 0.05 (5% per batch for MODERATE supply)
    escalation_rate_per_batch = 0.05 * supply_multiplier  # 7.5% for LIMITED

    batches = []
    total_cost = 0
    units_remaining = units
    batch_num = 0

    while units_remaining > 0:
        batch_num += 1
        batch_size = min(units_remaining, trade_volume)

        # Calculate price for this batch
        # Each batch sees escalation based on how many batches came before
        price_multiplier = 1.0 + (escalation_rate_per_batch * (batch_num - 1))
        batch_price = int(base_price * price_multiplier)
        batch_cost = batch_price * batch_size

        batches.append({
            'batch_num': batch_num,
            'units': batch_size,
            'price_per_unit': batch_price,
            'total_cost': batch_cost
        })

        total_cost += batch_cost
        units_remaining -= batch_size

    # Calculate statistics
    avg_price = total_cost / units if units > 0 else 0
    price_escalation_pct = ((avg_price - base_price) / base_price * 100) if base_price > 0 else 0

    breakdown = {
        'batches': batches,
        'price_escalation_pct': round(price_escalation_pct, 1),
        'avg_price_per_unit': int(avg_price)
    }

    return total_cost, breakdown


def calculate_batch_sale_revenue(
    base_price: int,
    units: int,
    trade_volume: int,
    activity: Optional[str] = None
) -> tuple[int, Dict]:
    """
    Calculate realistic total revenue when selling in batches, accounting for price degradation.

    When selling large quantities, each transaction increases supply, causing prices to fall.
    This models the price impact based on:
    - tradeVolume: Market liquidity depth (larger = more stable prices)
    - activity: Market activity level (WEAK/FAIR/STRONG/EXCESSIVE)

    Based on real-world data:
    - X1-TX46-J55: 21 units ASSAULT_RIFLES (tradeVolume=10, WEAK)
      - Minimal degradation due to matching tradeVolume
    - X1-TX46-H49: 18 units SHIP_PLATING (tradeVolume=6, WEAK) [estimated]
      - ~33% revenue loss due to WEAK activity and volume mismatch

    Args:
        base_price: Market's current purchase price (what we receive)
        units: Total units to sell
        trade_volume: Market's tradeVolume (batch size before price impact)
        activity: Market's activity level (affects buyer demand)

    Returns:
        tuple: (total_revenue, breakdown_dict)
        - total_revenue: Realistic total credits received
        - breakdown: Batch-by-batch pricing details with degradation %
    """
    if units <= 0 or base_price <= 0:
        return 0, {'batches': [], 'price_degradation_pct': 0, 'avg_price_per_unit': 0}

    activity_multiplier = ACTIVITY_MULTIPLIERS.get(activity or "STRONG", 1.0)

    # Calculate price degradation rate - CALIBRATED TO REAL-WORLD DATA
    # Real-world observations show MINIMAL degradation (markets more stable than expected):
    # - ASSAULT_RIFLES: 21u / 10tv = 2.1x, WEAK → -0.5% total
    # - SHIP_PLATING: 18u / 6tv = 3x, WEAK → -2.9% total (NOT -33%!)
    # - ADVANCED_CIRCUITRY: 20u / 20tv = 1x, RESTRICTED → -1.9% total
    # - FIREARMS: 21u / 10tv = 2.1x, WEAK → -1.0% total
    # - PRECIOUS_STONES: 42u / 60tv = 0.7x, WEAK → -1.1% total
    #
    # Pattern: Very conservative degradation, approximately 1% per tradeVolume multiple
    # Formula: degradation_pct = volume_ratio * activity_multiplier * 1.0
    #
    # Where:
    # - volume_ratio = units / tradeVolume (1.0 = exact match, 2.0 = 2x volume, etc.)
    # - activity_multiplier scales by market liquidity
    # - No base rate - degradation scales linearly from 0%
    #
    # Examples:
    # - 20u / 20tv = 1.0x, RESTRICTED (1.5): 1.0 * 1.5 = 1.5% ✓ (vs -1.9% actual)
    # - 18u / 6tv = 3.0x, WEAK (1.0): 3.0 * 1.0 = 3.0% ✓ (vs -2.9% actual)
    # - 21u / 10tv = 2.1x, WEAK (1.0): 2.1 * 1.0 = 2.1% (vs -0.5% and -1.0% actual - tolerant)
    # - 42u / 60tv = 0.7x, WEAK (1.0): 0.7 * 1.0 = 0.7% ✓ (vs -1.1% actual)
    volume_ratio = units / trade_volume if trade_volume > 0 else 1.0

    # Simple linear degradation based on volume ratio
    total_degradation_pct = volume_ratio * activity_multiplier

    # Calculate average price after degradation (simple linear model)
    # Instead of per-batch compounding, apply total degradation uniformly
    # This matches real-world observations where degradation is gradual but consistent
    degradation_factor = 1.0 - (total_degradation_pct / 100.0)
    avg_price_per_unit = max(1, int(base_price * degradation_factor))

    # Calculate total revenue
    total_revenue = avg_price_per_unit * units

    # Build batch breakdown for transparency
    batches = []
    units_remaining = units
    batch_num = 0

    while units_remaining > 0:
        batch_num += 1
        batch_size = min(units_remaining, trade_volume)
        batch_revenue = avg_price_per_unit * batch_size

        batches.append({
            'batch_num': batch_num,
            'units': batch_size,
            'price_per_unit': avg_price_per_unit,
            'total_revenue': batch_revenue
        })

        units_remaining -= batch_size

    # Calculate statistics
    avg_price = total_revenue / units if units > 0 else 0
    price_degradation_pct = ((base_price - avg_price) / base_price * 100) if base_price > 0 else 0

    breakdown = {
        'batches': batches,
        'price_degradation_pct': round(price_degradation_pct, 1),
        'avg_price_per_unit': int(avg_price)
    }

    return total_revenue, breakdown
