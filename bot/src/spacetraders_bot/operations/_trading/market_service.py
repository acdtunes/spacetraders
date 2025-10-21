"""
Market Data Service

Handles all market-related operations including price updates, validation, and queries.
Follows Single Responsibility Principle - only market data concerns.
"""

import logging
from datetime import datetime, timezone
from typing import Dict, List, Optional, Tuple

from spacetraders_bot.operations._trading.models import MultiLegRoute


def estimate_sell_price_with_degradation(base_price: int, units: int) -> int:
    """
    Estimate effective average sell price accounting for batch selling degradation.

    CALIBRATED TO REAL-WORLD DATA from STARHOPPER-1 execution (2025-10-12):
    - 18u SHIP_PLATING (tradeVolume=6): -2.9% degradation (NOT -33%!)
    - 21u ASSAULT_RIFLES (tradeVolume=10): -0.5% degradation
    - 20u ADVANCED_CIRCUITRY (tradeVolume=20): -1.9% degradation

    Conservative degradation model (assumes WEAK market activity):
    - Formula: degradation_pct = (units / 20) * 1.0  # Assumes tradeVolume ~20
    - This gives ~1% degradation per 20 units
    - Caps at 5% max for very large quantities

    Args:
        base_price: Market's listed purchase price (from database)
        units: Total units to be sold

    Returns:
        Effective average price per unit after degradation
    """
    volume_ratio = units / 20.0
    degradation_pct = min(volume_ratio * 1.0, 5.0)  # Cap at 5%
    effective_price = int(base_price * (1 - degradation_pct / 100.0))
    return effective_price


def find_planned_sell_price(good: str, route: MultiLegRoute, current_segment_index: int) -> Optional[int]:
    """
    Find the planned sell price for a good in future route segments

    CRITICAL: Used by circuit breaker to validate profitability of purchase
    Compares actual buy price vs planned sell price, NOT vs database cache

    Args:
        good: Trade good symbol (e.g., 'ALUMINUM_ORE')
        route: MultiLegRoute containing all segments
        current_segment_index: Index of segment where we're buying

    Returns:
        Planned sell price per unit if found, None otherwise
    """
    for segment_index in range(current_segment_index + 1, len(route.segments)):
        segment = route.segments[segment_index]
        for action in segment.actions_at_destination:
            if action.action == 'SELL' and action.good == good:
                return action.price_per_unit
    return None


def find_planned_sell_destination(
    good: str,
    route: MultiLegRoute,
    current_segment_index: int
) -> Optional[str]:
    """
    Find the planned sell destination waypoint for a good in future route segments

    CRITICAL: Used by cargo cleanup to navigate to the planned sell market
    instead of selling at current (often terrible) market prices

    Args:
        good: Trade good symbol (e.g., 'ALUMINUM')
        route: MultiLegRoute containing all segments
        current_segment_index: Index of segment where circuit breaker triggered

    Returns:
        Planned sell waypoint if found, None otherwise
    """
    for segment_index in range(current_segment_index + 1, len(route.segments)):
        segment = route.segments[segment_index]
        for action in segment.actions_at_destination:
            if action.action == 'SELL' and action.good == good:
                return action.waypoint
    return None


def update_market_price_from_transaction(
    db,
    waypoint: str,
    good: str,
    transaction_type: str,
    price_per_unit: int,
    logger: logging.Logger
) -> None:
    """
    Update database with real transaction price after purchase or sale

    CRITICAL: This ensures database reflects ACTUAL transaction prices, not stale GET /market prices

    Args:
        db: Database instance
        waypoint: Waypoint symbol where transaction occurred
        good: Trade good symbol
        transaction_type: 'PURCHASE' or 'SELL'
        price_per_unit: Actual price per unit from transaction response
        logger: Logger instance
    """
    try:
        timestamp = datetime.now(timezone.utc).strftime('%Y-%m-%dT%H:%M:%S.%f')[:-3] + 'Z'

        with db.transaction() as conn:
            if transaction_type == 'PURCHASE':
                # We bought from market, so this is the market's SELL price
                existing = _get_existing_market_data(conn, waypoint, good, ['supply', 'activity', 'purchase_price', 'trade_volume'])

                if existing:
                    supply, activity, purchase_price, trade_volume = existing
                    db.update_market_data(
                        conn,
                        waypoint_symbol=waypoint,
                        good_symbol=good,
                        supply=supply,
                        activity=activity,
                        purchase_price=purchase_price,  # Preserve existing purchase price
                        sell_price=price_per_unit,  # Update with actual transaction price
                        trade_volume=trade_volume,
                        last_updated=timestamp
                    )
                else:
                    db.update_market_data(
                        conn,
                        waypoint_symbol=waypoint,
                        good_symbol=good,
                        supply=None,
                        activity=None,
                        purchase_price=None,
                        sell_price=price_per_unit,
                        trade_volume=0,
                        last_updated=timestamp
                    )
                logger.info(f"  📊 Updated DB: {waypoint} sells {good} @ {price_per_unit:,} cr/unit")

            elif transaction_type == 'SELL':
                # We sold to market, so this is the market's PURCHASE price
                existing = _get_existing_market_data(conn, waypoint, good, ['supply', 'activity', 'sell_price', 'trade_volume'])

                if existing:
                    supply, activity, sell_price, trade_volume = existing
                    db.update_market_data(
                        conn,
                        waypoint_symbol=waypoint,
                        good_symbol=good,
                        supply=supply,
                        activity=activity,
                        purchase_price=price_per_unit,  # Update with actual transaction price
                        sell_price=sell_price,  # Preserve existing sell price
                        trade_volume=trade_volume,
                        last_updated=timestamp
                    )
                else:
                    db.update_market_data(
                        conn,
                        waypoint_symbol=waypoint,
                        good_symbol=good,
                        supply=None,
                        activity=None,
                        purchase_price=price_per_unit,
                        sell_price=None,
                        trade_volume=0,
                        last_updated=timestamp
                    )
                logger.info(f"  📊 Updated DB: {waypoint} buys {good} @ {price_per_unit:,} cr/unit")

    except Exception as e:
        logger.warning(f"  ⚠️  Failed to update market price in database: {e}")
        # Non-fatal - continue execution


def _get_existing_market_data(conn, waypoint: str, good: str, fields: List[str]) -> Optional[Tuple]:
    """
    Helper to safely extract existing market data from database

    Args:
        conn: Database connection
        waypoint: Waypoint symbol
        good: Good symbol
        fields: List of field names to extract

    Returns:
        Tuple of field values if found, None otherwise
    """
    try:
        query = f"SELECT {', '.join(fields)} FROM market_data WHERE waypoint_symbol = ? AND good_symbol = ?"
        cursor = conn.execute(query, (waypoint, good))
        existing = cursor.fetchone()
    except (AttributeError, TypeError):
        # Mock database or query failure
        return None

    if not existing:
        return None

    try:
        # Extract values safely handling different row types
        return tuple(existing[i] if len(existing) > i else None for i in range(len(fields)))
    except TypeError:
        # Mock object or other non-subscriptable type
        return None


def validate_market_data_freshness(
    db,
    route: MultiLegRoute,
    logger: logging.Logger,
    stale_threshold_hours: float = 1.0,
    aging_threshold_hours: float = 0.5
) -> Tuple[bool, List[Tuple], List[Tuple]]:
    """
    Validate that all market data in route is fresh enough for execution

    Args:
        db: Database instance
        route: MultiLegRoute to validate
        logger: Logger instance
        stale_threshold_hours: Hours after which data is considered stale (abort)
        aging_threshold_hours: Hours after which data is considered aging (warn)

    Returns:
        Tuple of (is_valid, stale_markets, aging_markets)
        - is_valid: True if no stale data found
        - stale_markets: List of (waypoint, good, age_hours) tuples for stale data
        - aging_markets: List of (waypoint, good, age_hours) tuples for aging data
    """
    logger.info("=" * 70)
    logger.info("PRE-FLIGHT MARKET DATA VALIDATION")
    logger.info("=" * 70)

    stale_markets = []
    aging_markets = []

    with db.connection() as conn:
        for segment in route.segments:
            for action in segment.actions_at_destination:
                waypoint = action.waypoint
                good = action.good

                market_data = db.get_market_data(conn, waypoint, good)
                if not market_data or len(market_data) == 0:
                    logger.warning(f"⚠️  No market data found for {waypoint} {good}")
                    continue

                last_updated = market_data[0].get('last_updated')
                if not last_updated:
                    logger.warning(f"⚠️  No timestamp for {waypoint} {good} (skipping freshness check)")
                    continue

                try:
                    timestamp = datetime.strptime(last_updated, '%Y-%m-%dT%H:%M:%S.%fZ').replace(tzinfo=timezone.utc)
                    age_hours = (datetime.now(timezone.utc) - timestamp).total_seconds() / 3600

                    if age_hours > stale_threshold_hours:
                        stale_markets.append((waypoint, good, age_hours))
                        logger.error(f"  ❌ STALE: {waypoint} {good} ({age_hours:.1f}h old)")
                    elif age_hours > aging_threshold_hours:
                        aging_markets.append((waypoint, good, age_hours))
                        logger.warning(f"  ⚠️  AGING: {waypoint} {good} ({age_hours:.1f}h old)")
                    else:
                        logger.info(f"  ✅ FRESH: {waypoint} {good} ({age_hours*60:.0f}min old)")
                except (ValueError, TypeError) as e:
                    logger.warning(f"  ⚠️  Invalid timestamp for {waypoint} {good}: {e}")

    if stale_markets:
        logger.error("")
        logger.error("=" * 70)
        logger.error("🚨 PRE-FLIGHT VALIDATION FAILED: STALE MARKET DATA")
        logger.error("=" * 70)
        logger.error(f"Found {len(stale_markets)} markets with stale data (>{stale_threshold_hours}h old):")
        for waypoint, good, age_hours in stale_markets:
            logger.error(f"  - {waypoint} {good}: {age_hours:.1f}h old")
        logger.error("")
        logger.error("RECOMMENDATION: Wait for scout fleet to refresh market data")
        logger.error("🛑 Route execution ABORTED to prevent trading with stale prices")
        logger.error("=" * 70)
        return False, stale_markets, aging_markets

    if aging_markets:
        logger.warning("")
        logger.warning("⏰ WARNING: Some market data is aging:")
        for waypoint, good, age_hours in aging_markets:
            logger.warning(f"  - {waypoint} {good}: {age_hours:.1f}h old")
        logger.warning("  Proceeding with caution - prices may have shifted")
        logger.warning("")
    else:
        logger.info("✅ All market data is fresh")

    logger.info("=" * 70)
    return True, stale_markets, aging_markets
