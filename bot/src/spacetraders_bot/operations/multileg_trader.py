#!/usr/bin/env python3
"""
Multi-leg trading optimization system

This module implements intelligent multi-stop trade routes that:
1. Buy goods at one market
2. Sell at next market if profitable, OR
3. Buy additional goods for future markets
4. Optimize total profit across entire route
"""

import heapq
import json
import logging
import time
from dataclasses import dataclass
from typing import Callable, Dict, List, Optional, Tuple

from datetime import datetime, timezone

from spacetraders_bot.core.api_client import APIClient
from spacetraders_bot.core.ship import ShipController
from spacetraders_bot.core.smart_navigator import SmartNavigator
from spacetraders_bot.core.utils import parse_waypoint_symbol, calculate_distance
from spacetraders_bot.operations.control import CircuitBreaker


def _update_market_price_from_transaction(
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
                # Query existing data to preserve purchase_price and other fields
                try:
                    cursor = conn.execute(
                        "SELECT supply, activity, purchase_price, trade_volume FROM market_data WHERE waypoint_symbol = ? AND good_symbol = ?",
                        (waypoint, good)
                    )
                    existing = cursor.fetchone()
                except (AttributeError, TypeError):
                    # Mock database or query failure - use defaults
                    existing = None

                if existing:
                    try:
                        supply = existing[0] if len(existing) > 0 else None
                        activity = existing[1] if len(existing) > 1 else None
                        purchase_price = existing[2] if len(existing) > 2 else None
                        trade_volume = existing[3] if len(existing) > 3 else 0
                    except TypeError:
                        # Mock object or other non-subscriptable type
                        supply, activity, purchase_price, trade_volume = None, None, None, 0

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
                    # No existing data - insert with transaction price
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
                try:
                    cursor = conn.execute(
                        "SELECT supply, activity, sell_price, trade_volume FROM market_data WHERE waypoint_symbol = ? AND good_symbol = ?",
                        (waypoint, good)
                    )
                    existing = cursor.fetchone()
                except (AttributeError, TypeError):
                    # Mock database or query failure - use defaults
                    existing = None

                if existing:
                    try:
                        supply = existing[0] if len(existing) > 0 else None
                        activity = existing[1] if len(existing) > 1 else None
                        sell_price = existing[2] if len(existing) > 2 else None
                        trade_volume = existing[3] if len(existing) > 3 else 0
                    except TypeError:
                        # Mock object or other non-subscriptable type
                        supply, activity, sell_price, trade_volume = None, None, None, 0

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


@dataclass
class TradeAction:
    """Represents a buy or sell action at a market"""
    waypoint: str
    good: str
    action: str  # 'BUY' or 'SELL'
    units: int
    price_per_unit: int
    total_value: int


@dataclass
class RouteSegment:
    """Represents one leg of a multi-stop trade route with dependency tracking"""
    from_waypoint: str
    to_waypoint: str
    distance: int
    fuel_cost: int
    actions_at_destination: List[TradeAction]  # What to do when we arrive
    cargo_after: Dict[str, int]  # Cargo state after actions at destination
    credits_after: int  # Credits after actions
    cumulative_profit: int  # Profit up to this point

    # NEW: Dependency tracking for smart skip logic
    depends_on_segments: List[int] = None  # Indices of segments this depends on
    independent_of_segments: List[int] = None  # Indices of segments this is independent from
    goods_involved: set = None  # Trade goods involved in this segment
    markets_involved: set = None  # Markets involved in this segment
    required_cargo_from_prior: Dict[str, int] = None  # Cargo required from prior segments
    can_skip_if_failed: bool = True  # Whether segment can be skipped without breaking route

    def __post_init__(self):
        """Initialize mutable default fields"""
        if self.depends_on_segments is None:
            self.depends_on_segments = []
        if self.independent_of_segments is None:
            self.independent_of_segments = []
        if self.goods_involved is None:
            self.goods_involved = set()
        if self.markets_involved is None:
            self.markets_involved = set()
        if self.required_cargo_from_prior is None:
            self.required_cargo_from_prior = {}


@dataclass
class MultiLegRoute:
    """Complete multi-leg trading route"""
    segments: List[RouteSegment]
    total_profit: int
    total_distance: int
    total_fuel_cost: int
    estimated_time_minutes: int

@dataclass
class MarketEvaluation:
    """Represents the result of evaluating actions at a market."""

    actions: List[TradeAction]
    cargo_after: Dict[str, int]
    credits_after: int
    net_profit: int


@dataclass
class SegmentDependency:
    """Captures dependencies between route segments for smart skip logic"""
    segment_index: int
    depends_on: List[int]  # Indices of prerequisite segments
    dependency_type: str  # 'CARGO', 'CREDIT', or 'NONE'
    required_cargo: Dict[str, int]  # {good: units} needed from prior segments
    required_credits: int  # Minimum credits needed to execute
    can_skip: bool  # True if segment can be skipped without breaking route


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

    NOTE: This is a simplified estimate. For accurate pricing, use
    calculate_batch_sale_revenue() from market_data.py with actual market data.

    Args:
        base_price: Market's listed purchase price (from database)
        units: Total units to be sold

    Returns:
        Effective average price per unit after degradation

    Examples:
        >>> estimate_sell_price_with_degradation(8000, 20)
        7920  # -1% degradation for 20 units

        >>> estimate_sell_price_with_degradation(8000, 40)
        7840  # -2% degradation for 40 units
    """
    # Conservative linear degradation: 1% per 20 units
    # Assumes moderate tradeVolume (~20) and WEAK market activity
    volume_ratio = units / 20.0
    degradation_pct = min(volume_ratio * 1.0, 5.0)  # Cap at 5%

    # Return effective average price
    effective_price = int(base_price * (1 - degradation_pct / 100.0))
    return effective_price


def _find_planned_sell_price(good: str, route: MultiLegRoute, current_segment_index: int) -> Optional[int]:
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

    Example:
        # Segment 0: BUY ALUMINUM @ E45 for 68 cr (planned)
        # Segment 1: SELL ALUMINUM @ D42 for 558 cr (planned)
        # _find_planned_sell_price('ALUMINUM_ORE', route, 0) → 558
    """
    # Search future segments for SELL action of this good
    for segment_index in range(current_segment_index + 1, len(route.segments)):
        segment = route.segments[segment_index]
        for action in segment.actions_at_destination:
            if action.action == 'SELL' and action.good == good:
                # Found planned sell action - return price per unit
                return action.price_per_unit

    # No sell action found in remaining route
    return None


def _find_planned_sell_destination(
    good: str,
    route: 'MultiLegRoute',
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

    Example:
        # Segment 0: At E45, BUY ALUMINUM @ 140 cr (circuit breaker triggers)
        # Segment 1: E45 → D42, SELL ALUMINUM @ 558 cr (planned but not executed)
        # _find_planned_sell_destination('ALUMINUM', route, 0) → 'X1-JB26-D42'
    """
    # Search future segments for SELL action of this good
    for segment_index in range(current_segment_index + 1, len(route.segments)):
        segment = route.segments[segment_index]
        for action in segment.actions_at_destination:
            if action.action == 'SELL' and action.good == good:
                # Found planned sell action - return the waypoint
                return action.waypoint

    # No sell action found in remaining route
    return None


def _cleanup_stranded_cargo(
    ship: ShipController,
    api: APIClient,
    db,
    logger: logging.Logger,
    route: Optional['MultiLegRoute'] = None,
    current_segment_index: Optional[int] = None,
    unprofitable_item: Optional[str] = None
) -> bool:
    """
    Emergency cleanup: Sell unprofitable cargo using intelligent market search

    Called by circuit breakers before exiting to prevent stranded cargo.
    CRITICAL FIX: Only salvages unprofitable items, keeps cargo destined for future profitable segments.

    Implements tiered salvage strategy:
    1. If unprofitable_item specified, ONLY salvage that item (selective salvage)
    2. For salvaged items:
       a. Check route for planned sell destination (HIGHEST PRIORITY - avoids selling at buy market)
       b. If planned destination found, navigate there and sell at planned price
       c. Otherwise, check if current market buys the good
       d. If not, search for nearby buyers (<200 units away)
       e. Navigate to nearby buyer if found
       f. Otherwise, log warning and skip unsellable goods
    3. Keep all other cargo intact for future segments

    Args:
        ship: ShipController instance
        api: APIClient instance
        db: Database instance for market lookups
        logger: Logger for recording cleanup actions
        route: Optional MultiLegRoute containing planned actions
        current_segment_index: Optional index of segment where circuit breaker triggered
        unprofitable_item: Optional specific trade good that triggered circuit breaker
                          If specified, only this item is salvaged (others are kept)
                          If None, all cargo is salvaged (legacy behavior for backward compatibility)

    Returns:
        True if cleanup succeeded (or no cargo to clean), False on critical failure
    """
    try:
        # Get current ship status
        ship_data = ship.get_status()
        if not ship_data:
            logger.error("Failed to get ship status for cargo cleanup")
            return False

        cargo = ship_data.get('cargo', {})
        inventory = cargo.get('inventory', [])

        # If no cargo, nothing to clean up
        if not inventory or cargo.get('units', 0) == 0:
            logger.info("No stranded cargo to clean up")
            return True

        # SELECTIVE SALVAGE: If unprofitable_item specified, only salvage that item
        if unprofitable_item:
            logger.warning("="*70)
            logger.warning(f"🧹 SELECTIVE CARGO CLEANUP: Salvaging only {unprofitable_item}")
            logger.warning("="*70)
            logger.warning(f"  Unprofitable item: {unprofitable_item}")
            logger.warning("  Other cargo will be KEPT for future profitable segments")
            logger.warning("="*70)

            # Filter inventory to only process the unprofitable item
            items_to_salvage = [item for item in inventory if item['symbol'] == unprofitable_item]
            items_to_keep = [item for item in inventory if item['symbol'] != unprofitable_item]

            if not items_to_salvage:
                logger.warning(f"  ⚠️  {unprofitable_item} not found in cargo - nothing to salvage")
                return True

            if items_to_keep:
                logger.info("  Items being KEPT for future segments:")
                for item in items_to_keep:
                    logger.info(f"    - {item['units']}x {item['symbol']}")

            # Only salvage the unprofitable item
            inventory = items_to_salvage
        else:
            # Legacy behavior: salvage ALL cargo
            logger.warning("="*70)
            logger.warning("🧹 CARGO CLEANUP: Selling ALL stranded cargo")
            logger.warning("="*70)

        # Log cargo to be salvaged
        for item in inventory:
            logger.warning(f"  Salvaging: {item['units']}x {item['symbol']}")

        # Get current location
        current_waypoint = ship_data['nav']['waypointSymbol']
        system = ship_data['nav']['systemSymbol']

        logger.info(f"Attempting cleanup at current location: {current_waypoint}")

        # Ensure ship is docked
        if ship_data['nav']['status'] != 'DOCKED':
            logger.info("Docking for cargo cleanup...")
            if not ship.dock():
                logger.error("Failed to dock for cargo cleanup")
                return False

        # Sell all cargo with intelligent market selection
        cleanup_revenue = 0
        items_to_process = list(inventory)  # Copy list to avoid modification during iteration

        for item in items_to_process:
            good = item['symbol']
            units = item['units']

            # STEP 1: Check if there's a planned sell destination in the route
            planned_sell_waypoint = None
            if route and current_segment_index is not None:
                logger.info(f"🔍 Searching for planned sell destination for {good} from segment {current_segment_index}")
                planned_sell_waypoint = _find_planned_sell_destination(good, route, current_segment_index)
                if planned_sell_waypoint:
                    logger.info(f"  Found: {planned_sell_waypoint} (current: {current_waypoint})")
                else:
                    logger.info(f"  No planned sell destination found in remaining segments")
            else:
                logger.info(f"  Skipping planned destination search (route={route is not None}, segment_index={current_segment_index})")

            if planned_sell_waypoint and planned_sell_waypoint != current_waypoint:
                # PRIORITY 1: Navigate to planned sell destination
                logger.warning(f"  🎯 Found planned sell destination for {good}: {planned_sell_waypoint}")
                logger.warning(f"  Navigating from {current_waypoint} to {planned_sell_waypoint}...")

                try:
                    # Create SmartNavigator for navigation
                    from spacetraders_bot.core.smart_navigator import SmartNavigator
                    navigator = SmartNavigator(api, system)

                    # Navigate to planned sell destination
                    if navigator.execute_route(ship, planned_sell_waypoint):
                        logger.warning(f"  ✅ Arrived at {planned_sell_waypoint}, docking...")
                        if ship.dock():
                            # Sell at planned destination (gets full planned price!)
                            transaction = ship.sell(good, units, check_market_prices=False)
                            if transaction:
                                revenue = transaction['totalPrice']
                                price_per_unit = revenue / units if units > 0 else 0
                                cleanup_revenue += revenue
                                logger.warning(f"  ✅ Sold {units}x {good} for {revenue:,} credits ({price_per_unit:.0f} cr/unit)")
                                logger.warning(f"  💰 Recovered value by navigating to planned destination!")

                                # Update current waypoint after successful navigation
                                current_waypoint = planned_sell_waypoint
                                continue  # Move to next item
                            else:
                                logger.error(f"  ❌ Failed to sell {good} at planned destination {planned_sell_waypoint}")
                        else:
                            logger.error(f"  ❌ Failed to dock at {planned_sell_waypoint}")
                    else:
                        logger.error(f"  ❌ Failed to navigate to {planned_sell_waypoint}")
                        logger.warning(f"  Falling back to current market or nearby buyers...")

                except Exception as e:
                    logger.error(f"  ❌ Error navigating to planned destination: {e}")
                    logger.warning(f"  Falling back to current market or nearby buyers...")

            # STEP 2: Check if current market buys this good (fallback if no planned destination)
            logger.info(f"Checking if {current_waypoint} buys {good}...")

            current_market_accepts = False
            try:
                with db.connection() as conn:
                    market_data = db.get_market_data(conn, current_waypoint, good)
                    if market_data and len(market_data) > 0:
                        # Check if market has a purchase_price (what they pay us)
                        if market_data[0].get('purchase_price', 0) > 0:
                            current_market_accepts = True
                            logger.info(f"  ✅ {current_waypoint} buys {good}")
                        else:
                            logger.warning(f"  ❌ {current_waypoint} doesn't buy {good} (no purchase price)")
                    else:
                        logger.warning(f"  ❌ {current_waypoint} doesn't buy {good} (not listed)")
            except Exception as e:
                logger.warning(f"  ⚠️  Failed to check market data: {e}")
                # If market check fails, try to sell anyway (might work)
                current_market_accepts = True  # Fallback to old behavior

            # If current market accepts, sell here
            if current_market_accepts:
                logger.warning(f"  Selling {units}x {good} at current market...")

                try:
                    # Use ship.sell with check_market_prices=False to accept any price
                    transaction = ship.sell(good, units, check_market_prices=False)

                    if transaction:
                        revenue = transaction['totalPrice']
                        price_per_unit = revenue / units if units > 0 else 0
                        cleanup_revenue += revenue
                        logger.warning(f"  ✅ Sold {units}x {good} for {revenue:,} credits ({price_per_unit:.0f} cr/unit)")
                    else:
                        logger.error(f"  ❌ Failed to sell {good}")

                except Exception as e:
                    logger.error(f"  ❌ Error selling {good}: {e}")
                    # Good remains in cargo - will be handled by nearby market search below

            else:
                # Current market doesn't buy this good - search for nearby buyer
                logger.warning(f"  Current market doesn't buy {good} - searching for nearby buyers...")

                try:
                    # Query database for markets that buy this good
                    with db.connection() as conn:
                        cursor = conn.cursor()

                        # Get current waypoint coordinates
                        cursor.execute("SELECT x, y FROM waypoints WHERE waypoint_symbol = ?", (current_waypoint,))
                        current_coords = cursor.fetchone()

                        if not current_coords:
                            logger.error(f"  ❌ Failed to get coordinates for {current_waypoint}")
                            continue

                        # Find markets that buy this good within 200 units
                        cursor.execute("""
                            SELECT
                                m.waypoint_symbol,
                                m.purchase_price,
                                w.x,
                                w.y,
                                ((w.x - ?) * (w.x - ?) + (w.y - ?) * (w.y - ?)) as distance_squared
                            FROM market_data m
                            JOIN waypoints w ON m.waypoint_symbol = w.waypoint_symbol
                            WHERE m.good_symbol = ?
                            AND m.purchase_price > 0
                            AND w.waypoint_symbol LIKE ?
                            ORDER BY distance_squared ASC
                            LIMIT 5
                        """, (
                            current_coords[0], current_coords[0],
                            current_coords[1], current_coords[1],
                            good,
                            f"{system}-%"
                        ))

                        nearby_buyers = cursor.fetchall()

                        if nearby_buyers:
                            # Calculate actual distance for best buyer
                            best_buyer_waypoint = nearby_buyers[0][0]
                            best_price = nearby_buyers[0][1]
                            best_coords = (nearby_buyers[0][2], nearby_buyers[0][3])

                            distance = ((best_coords[0] - current_coords[0])**2 +
                                      (best_coords[1] - current_coords[1])**2)**0.5

                            if distance <= 200:  # Within acceptable range
                                logger.warning(f"  🎯 Found buyer: {best_buyer_waypoint} ({distance:.0f} units away, {best_price:,} cr/unit)")
                                logger.warning(f"  Navigating to {best_buyer_waypoint}...")

                                # Create SmartNavigator for navigation
                                from spacetraders_bot.core.smart_navigator import SmartNavigator
                                navigator = SmartNavigator(api, system)

                                # Navigate to buyer market
                                if navigator.execute_route(ship, best_buyer_waypoint):
                                    logger.warning(f"  Arrived at {best_buyer_waypoint}, docking...")
                                    if ship.dock():
                                        # Sell at buyer market
                                        transaction = ship.sell(good, units, check_market_prices=False)
                                        if transaction:
                                            revenue = transaction['totalPrice']
                                            price_per_unit = revenue / units if units > 0 else 0
                                            cleanup_revenue += revenue
                                            logger.warning(f"  ✅ Sold {units}x {good} for {revenue:,} credits ({price_per_unit:.0f} cr/unit)")
                                        else:
                                            logger.error(f"  ❌ Failed to sell {good} at {best_buyer_waypoint}")
                                    else:
                                        logger.error(f"  ❌ Failed to dock at {best_buyer_waypoint}")
                                else:
                                    logger.error(f"  ❌ Failed to navigate to {best_buyer_waypoint}")
                            else:
                                logger.warning(f"  ⚠️  Nearest buyer {best_buyer_waypoint} is too far ({distance:.0f} units)")
                                logger.warning(f"  Skipping {good} - no nearby buyers within 200 units")
                        else:
                            logger.warning(f"  ⚠️  No buyers found for {good} in system {system}")
                            logger.warning(f"  Skipping {good} - will remain in cargo")

                except Exception as e:
                    logger.error(f"  ❌ Error searching for nearby buyers: {e}")
                    import traceback
                    logger.error(traceback.format_exc())

        logger.warning(f"Total cleanup revenue: {cleanup_revenue:,} credits")
        logger.warning("="*70)

        # Verify final cargo state
        final_status = ship.get_status()
        if final_status:
            final_cargo = final_status.get('cargo', {})
            if final_cargo.get('units', 0) == 0:
                logger.info("✅ Cargo cleanup complete - ship hold empty")
                return True
            else:
                logger.warning(f"⚠️  Partial cleanup - {final_cargo.get('units', 0)} units remaining")
                # Log which goods remain
                for item in final_cargo.get('inventory', []):
                    logger.warning(f"  Remaining: {item['units']}x {item['symbol']}")
                return True  # Partial success is still acceptable

        return True

    except Exception as e:
        logger.error(f"Critical error during cargo cleanup: {e}")
        import traceback
        logger.error(traceback.format_exc())
        return False


class TradeEvaluationStrategy:
    """Defines the contract for market evaluation strategies."""

    def evaluate(
        self,
        *,
        market: str,
        current_cargo: Dict[str, int],
        current_credits: int,
        trade_opportunities: List[Dict],
        cargo_capacity: int,
        fuel_cost: int,
    ) -> MarketEvaluation:
        raise NotImplementedError


class ProfitFirstStrategy(TradeEvaluationStrategy):
    """Default strategy that maximizes profit by mixing sell/buy actions."""

    def __init__(self, logger: logging.Logger):
        self.logger = logger

    def evaluate(
        self,
        *,
        market: str,
        current_cargo: Dict[str, int],
        current_credits: int,
        trade_opportunities: List[Dict],
        cargo_capacity: int,
        fuel_cost: int,
    ) -> MarketEvaluation:
        sell_actions, cargo_after_sell, credits_after_sell, revenue = self._apply_sell_actions(
            market=market,
            current_cargo=current_cargo,
            current_credits=current_credits,
            trade_opportunities=trade_opportunities,
        )

        buy_actions, cargo_after_buy, credits_after_buy, purchase_costs = self._apply_buy_actions(
            market=market,
            trade_opportunities=trade_opportunities,
            cargo=cargo_after_sell,
            credits=credits_after_sell,
            cargo_capacity=cargo_capacity,
        )

        potential_future_revenue = self._estimate_potential_future_revenue(
            cargo_after_buy,
            market,
            trade_opportunities,
        )

        net_profit = revenue - fuel_cost + (potential_future_revenue - purchase_costs)

        return MarketEvaluation(
            actions=sell_actions + buy_actions,
            cargo_after=cargo_after_buy,
            credits_after=credits_after_buy,
            net_profit=net_profit,
        )

    def _apply_sell_actions(
        self,
        *,
        market: str,
        current_cargo: Dict[str, int],
        current_credits: int,
        trade_opportunities: List[Dict],
    ) -> Tuple[List[TradeAction], Dict[str, int], int, int]:
        actions: List[TradeAction] = []
        cargo = current_cargo.copy()
        credits = current_credits
        revenue = 0

        for good, units in list(cargo.items()):
            sell_opp = next((o for o in trade_opportunities if o['sell_waypoint'] == market and o['good'] == good), None)
            if not sell_opp:
                continue

            sell_price = sell_opp['sell_price']
            trade_volume = sell_opp['trade_volume']

            units_to_sell = min(units, trade_volume)
            if units_to_sell <= 0:
                continue

            # DEGRADATION FIX: Apply price degradation model to expected sell price
            # Large cargo quantities experience ~0.23% per-unit price erosion during batch selling
            effective_sell_price = estimate_sell_price_with_degradation(sell_price, units_to_sell)
            sale_value = units_to_sell * effective_sell_price

            self.logger.debug(
                "Sell %s: %s units @ %s (cached: %s, degradation: %.1f%%) = %s",
                good,
                units_to_sell,
                effective_sell_price,
                sell_price,
                ((sell_price - effective_sell_price) / sell_price * 100) if sell_price > 0 else 0,
                sale_value,
            )
            actions.append(TradeAction(
                waypoint=market,
                good=good,
                action='SELL',
                units=units_to_sell,
                price_per_unit=effective_sell_price,  # Use degraded price for accurate profit calculation
                total_value=sale_value,
            ))

            credits += sale_value
            revenue += sale_value
            cargo[good] -= units_to_sell
            if cargo[good] == 0:
                del cargo[good]

        return actions, cargo, credits, revenue

    def _apply_buy_actions(
        self,
        *,
        market: str,
        trade_opportunities: List[Dict],
        cargo: Dict[str, int],
        credits: int,
        cargo_capacity: int,
    ) -> Tuple[List[TradeAction], Dict[str, int], int, int]:
        actions: List[TradeAction] = []
        updated_cargo = cargo.copy()
        updated_credits = credits
        purchase_cost = 0

        cargo_used = sum(updated_cargo.values())
        cargo_available = cargo_capacity - cargo_used

        for opp in trade_opportunities:
            if opp['buy_waypoint'] != market:
                continue

            if cargo_available <= 0 or updated_credits <= 0:
                break

            good = opp['good']
            buy_price = opp['buy_price']
            trade_volume = opp['trade_volume']

            if buy_price <= 0:
                continue

            max_affordable = min(updated_credits // buy_price, cargo_available, trade_volume)
            if max_affordable <= 0:
                continue

            purchase_value = max_affordable * buy_price
            actions.append(TradeAction(
                waypoint=market,
                good=good,
                action='BUY',
                units=max_affordable,
                price_per_unit=buy_price,
                total_value=purchase_value,
            ))

            updated_credits -= purchase_value
            updated_cargo[good] = updated_cargo.get(good, 0) + max_affordable
            cargo_available -= max_affordable
            purchase_cost += purchase_value

        return actions, updated_cargo, updated_credits, purchase_cost

    def _estimate_potential_future_revenue(
        self,
        cargo: Dict[str, int],
        current_market: str,
        trade_opportunities: List[Dict],
    ) -> int:
        potential_revenue = 0
        for good, units in cargo.items():
            best_sell = max(
                (
                    o['sell_price']
                    for o in trade_opportunities
                    if o['good'] == good and o['sell_waypoint'] != current_market
                ),
                default=0,
            )
            # DEGRADATION FIX: Apply price degradation to future sell price estimate
            effective_sell_price = estimate_sell_price_with_degradation(best_sell, units)
            potential_revenue += units * effective_sell_price
        return potential_revenue


class GreedyRoutePlanner:
    """Encapsulates the greedy multi-leg route search logic."""

    def __init__(self, logger: logging.Logger, db, strategy: Optional[TradeEvaluationStrategy] = None):
        self.logger = logger
        self.db = db
        self.strategy = strategy or ProfitFirstStrategy(logger)

    def find_route(
        self,
        start_waypoint: str,
        markets: List[str],
        trade_opportunities: List[Dict],
        max_stops: int,
        cargo_capacity: int,
        starting_credits: int,
        ship_speed: int,
        starting_cargo: Optional[Dict[str, int]] = None,
    ) -> Optional[MultiLegRoute]:
        current_waypoint = start_waypoint
        # CRITICAL FIX: Account for residual cargo from previous operations
        # Ship may have existing cargo when route planning starts
        current_cargo: Dict[str, int] = starting_cargo.copy() if starting_cargo else {}
        current_credits = starting_credits
        cumulative_profit = 0
        route_segments: List[RouteSegment] = []
        visited = {start_waypoint}

        for _ in range(max_stops):
            next_step = self._find_best_next_market(
                current_waypoint=current_waypoint,
                current_cargo=current_cargo,
                current_credits=current_credits,
                markets=markets,
                trade_opportunities=trade_opportunities,
                cargo_capacity=cargo_capacity,
                visited=visited,
            )

            if not next_step:
                break

            next_waypoint, actions, new_cargo, new_credits, segment_profit, distance = next_step

            segment = RouteSegment(
                from_waypoint=current_waypoint,
                to_waypoint=next_waypoint,
                distance=distance,
                fuel_cost=int(distance * 1.1),
                actions_at_destination=actions,
                cargo_after=new_cargo.copy(),
                credits_after=new_credits,
                cumulative_profit=cumulative_profit + segment_profit,
            )

            route_segments.append(segment)

            current_waypoint = next_waypoint
            current_cargo = new_cargo
            current_credits = new_credits
            cumulative_profit += segment_profit
            visited.add(next_waypoint)

        if not route_segments:
            return None

        total_distance = sum(s.distance for s in route_segments)
        total_fuel_cost = sum(s.fuel_cost for s in route_segments)
        total_profit = cumulative_profit - total_fuel_cost
        estimated_time_minutes = (total_distance / ship_speed) * 60

        return MultiLegRoute(
            segments=route_segments,
            total_profit=total_profit,
            total_distance=total_distance,
            total_fuel_cost=total_fuel_cost,
            estimated_time_minutes=estimated_time_minutes,
        )

    def _find_best_next_market(
        self,
        current_waypoint: str,
        current_cargo: Dict[str, int],
        current_credits: int,
        markets: List[str],
        trade_opportunities: List[Dict],
        cargo_capacity: int,
        visited: set,
    ) -> Optional[Tuple]:
        best_option = None
        best_profit = 0

        for next_market in markets:
            if next_market in visited:
                continue

            distance = self._estimate_distance(current_waypoint, next_market)
            fuel_cost = int(distance * 1.1)

            evaluation = self.strategy.evaluate(
                market=next_market,
                current_cargo=current_cargo,
                current_credits=current_credits,
                trade_opportunities=trade_opportunities,
                cargo_capacity=cargo_capacity,
                fuel_cost=fuel_cost,
            )

            actions = evaluation.actions
            new_cargo = evaluation.cargo_after
            new_credits = evaluation.credits_after
            net_profit = evaluation.net_profit

            if net_profit > best_profit:
                best_profit = net_profit
                best_option = (next_market, actions, new_cargo, new_credits, net_profit, distance)

        return best_option

    def _estimate_distance(self, from_waypoint: str, to_waypoint: str) -> float:
        with self.db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("SELECT x, y FROM waypoints WHERE waypoint_symbol = ?", (from_waypoint,))
            from_row = cursor.fetchone()
            cursor.execute("SELECT x, y FROM waypoints WHERE waypoint_symbol = ?", (to_waypoint,))
            to_row = cursor.fetchone()

            if not from_row or not to_row:
                return 150.0

            dx = to_row[0] - from_row[0]
            dy = to_row[1] - from_row[1]
            return (dx**2 + dy**2) ** 0.5


class MultiLegTradeOptimizer:
    """
    Optimizes multi-leg trading routes using market data

    Strategy:
    1. Start at current location with empty cargo
    2. For each potential next market:
       - Check what we can buy/sell profitably
       - Calculate profit if we sell current cargo
       - Calculate profit if we buy new goods for future markets
    3. Use A* search to find optimal route
    """

    def __init__(
        self,
        api: APIClient,
        db,
        player_id: int,
        logger: Optional[logging.Logger] = None,
        strategy_factory: Optional[Callable[[logging.Logger], TradeEvaluationStrategy]] = None,
    ):
        self.api = api
        self.db = db
        self.player_id = player_id
        self.logger = logger or logging.getLogger(__name__)
        self._strategy_factory = strategy_factory or (lambda log: ProfitFirstStrategy(log))

    def find_optimal_route(
        self,
        start_waypoint: str,
        system: str,
        max_stops: int,
        cargo_capacity: int,
        starting_credits: int,
        ship_speed: int,
        fuel_capacity: int,
        current_fuel: int,
        starting_cargo: Optional[Dict[str, int]] = None,
    ) -> Optional[MultiLegRoute]:
        """
        Find the most profitable multi-leg trade route

        Args:
            start_waypoint: Current location
            system: System symbol (e.g., "X1-JB26")
            max_stops: Maximum number of stops (3-5 recommended)
            cargo_capacity: Ship cargo capacity
            starting_credits: Available credits for purchases
            ship_speed: Ship speed (for time estimation)
            fuel_capacity: Fuel tank capacity
            current_fuel: Current fuel level
            starting_cargo: Existing cargo from previous operations (residual)

        Returns:
            MultiLegRoute with optimal path, or None if no profitable route found
        """
        self.logger.info("="*70)
        self.logger.info("MULTI-LEG ROUTE OPTIMIZATION")
        self.logger.info("="*70)
        self.logger.info(f"Start: {start_waypoint}")
        self.logger.info(f"Max stops: {max_stops}")
        self.logger.info(f"Cargo capacity: {cargo_capacity}")
        self.logger.info(f"Starting credits: {starting_credits:,}")
        self.logger.info("="*70)

        # Get all markets in system from database
        markets = self._get_markets_in_system(system)
        self.logger.info(f"Found {len(markets)} markets in {system}")

        if not markets:
            self.logger.error("No markets found in system")
            return None

        # Get all trade opportunities from database
        trade_opportunities = self._get_trade_opportunities(system, markets)
        self.logger.info(f"Found {len(trade_opportunities)} trade opportunities")

        strategy = self._strategy_factory(self.logger)
        planner = GreedyRoutePlanner(self.logger, self.db, strategy=strategy)
        best_route = planner.find_route(
            start_waypoint=start_waypoint,
            markets=markets,
            trade_opportunities=trade_opportunities,
            max_stops=max_stops,
            cargo_capacity=cargo_capacity,
            starting_credits=starting_credits,
            ship_speed=ship_speed,
            starting_cargo=starting_cargo,
        )

        if best_route:
            self.logger.info("\n" + "="*70)
            self.logger.info("OPTIMAL ROUTE FOUND")
            self.logger.info("="*70)
            self.logger.info(f"Total profit: {best_route.total_profit:,} credits")
            self.logger.info(f"Total distance: {best_route.total_distance:.0f} units")
            self.logger.info(f"Estimated time: {best_route.estimated_time_minutes:.0f} minutes")
            self.logger.info(f"Stops: {len(best_route.segments)}")
            self.logger.info("\nRoute:")

            for i, segment in enumerate(best_route.segments, 1):
                self.logger.info(f"\n  Stop {i}: {segment.to_waypoint}")
                self.logger.info(f"    Distance: {segment.distance:.0f} units")
                self.logger.info(f"    Actions:")
                for action in segment.actions_at_destination:
                    symbol = '💰' if action.action == 'BUY' else '💵'
                    self.logger.info(f"      {symbol} {action.action} {action.units}x {action.good} @ {action.price_per_unit:,} = {action.total_value:,}")
                self.logger.info(f"    Cargo after: {segment.cargo_after}")
                self.logger.info(f"    Cumulative profit: {segment.cumulative_profit:,}")

            self.logger.info("="*70)
        else:
            self.logger.warning("No profitable multi-leg route found")

        return best_route

    def _get_markets_in_system(self, system: str) -> List[str]:
        """Get all market waypoints in a system from database"""
        with self.db.connection() as conn:
            cursor = conn.cursor()
            cursor.execute("""
                SELECT DISTINCT waypoint_symbol
                FROM market_data
                WHERE waypoint_symbol LIKE ?
                AND (updated_by_player = ? OR updated_by_player IS NULL)
            """, (f"{system}-%", self.player_id))

            return [row[0] for row in cursor.fetchall()]

    def _get_trade_opportunities(self, system: str, markets: List[str]) -> List[Dict]:
        """
        Get all profitable trade opportunities from database

        Returns list of dicts with:
        - buy_waypoint, sell_waypoint
        - good
        - buy_price (what we pay), sell_price (what we receive)
        - spread (profit per unit)
        - trade_volume (transaction limit)
        """
        opportunities = []

        with self.db.connection() as conn:
            for buy_market in markets:
                buy_data = self.db.get_market_data(conn, buy_market, None)
                opportunities.extend(
                    self._collect_opportunities_for_market(
                        conn, buy_market, buy_data, markets
                    )
                )

        # Sort by spread (most profitable first)
        opportunities.sort(key=lambda x: x['spread'], reverse=True)

        return opportunities

    def _collect_opportunities_for_market(
        self,
        conn,
        buy_market: str,
        buy_data: List[Dict],
        markets: List[str],
    ) -> List[Dict]:
        opportunities = []

        for sell_market in markets:
            if sell_market == buy_market:
                continue

            for buy_record in buy_data:
                good = buy_record['good_symbol']
                # CRITICAL FIX: When we BUY from a market, we pay their SELL_PRICE (what they sell TO us)
                # Database field mapping:
                # - sell_price = what traders PAY to BUY from market (market's asking price)
                # - purchase_price = what market PAYS to BUY from traders (market's bid price)
                buy_price = buy_record.get('sell_price')

                if not buy_price:
                    continue

                # FRESHNESS CHECK: Validate buy market data age
                buy_last_updated = buy_record.get('last_updated')
                if buy_last_updated:
                    try:
                        buy_timestamp = datetime.strptime(buy_last_updated, '%Y-%m-%dT%H:%M:%S.%fZ').replace(tzinfo=timezone.utc)
                        buy_age_hours = (datetime.now(timezone.utc) - buy_timestamp).total_seconds() / 3600

                        # CRITICAL: Filter out stale buy market data (>1 hour old)
                        if buy_age_hours > 1.0:
                            self.logger.warning(f"⚠️  Skipping stale buy data: {buy_market} {good} ({buy_age_hours:.1f}h old)")
                            continue
                        elif buy_age_hours > 0.5:
                            self.logger.info(f"  ⏰ Aging buy data: {buy_market} {good} ({buy_age_hours:.1f}h old)")
                    except (ValueError, TypeError) as e:
                        self.logger.warning(f"  ⚠️  Invalid timestamp for {buy_market} {good}: {e}")

                sell_data = self.db.get_market_data(conn, sell_market, good)
                if not sell_data:
                    continue

                sell_record = sell_data[0]
                sell_price = sell_record.get('purchase_price')

                if not sell_price:
                    continue

                # FRESHNESS CHECK: Validate sell market data age
                sell_last_updated = sell_record.get('last_updated')
                if sell_last_updated:
                    try:
                        sell_timestamp = datetime.strptime(sell_last_updated, '%Y-%m-%dT%H:%M:%S.%fZ').replace(tzinfo=timezone.utc)
                        sell_age_hours = (datetime.now(timezone.utc) - sell_timestamp).total_seconds() / 3600

                        # CRITICAL: Filter out stale sell market data (>1 hour old)
                        if sell_age_hours > 1.0:
                            self.logger.warning(f"⚠️  Skipping stale sell data: {sell_market} {good} ({sell_age_hours:.1f}h old)")
                            continue
                        elif sell_age_hours > 0.5:
                            self.logger.info(f"  ⏰ Aging sell data: {sell_market} {good} ({sell_age_hours:.1f}h old)")
                    except (ValueError, TypeError) as e:
                        self.logger.warning(f"  ⚠️  Invalid timestamp for {sell_market} {good}: {e}")

                spread = sell_price - buy_price
                if spread <= 0:
                    continue

                opportunities.append({
                    'buy_waypoint': buy_market,
                    'sell_waypoint': sell_market,
                    'good': good,
                    'buy_price': buy_price,
                    'sell_price': sell_price,
                    'spread': spread,
                    'trade_volume': buy_record.get('trade_volume', 100),
                })

        return opportunities


class FleetTradeOptimizer:
    """
    Multi-ship fleet trade route optimizer with conflict avoidance

    Prevents (resource, waypoint) collisions between ships using greedy sequential assignment.
    Ensures each ship gets an independently profitable route while maximizing total fleet profit.

    Algorithm:
    1. Assign best route to Ship 1 (standard single-ship optimization)
    2. Record all (resource, waypoint) BUY pairs from Ship 1's route
    3. For Ship 2, exclude any trade opportunities that would buy same resource at same waypoint
    4. Repeat for Ship N

    Example:
        Ship 1: D42 (buy ADVANCED_CIRCUITRY) → A4 (sell)
        Ship 2: Cannot buy ADVANCED_CIRCUITRY at D42 (conflict!)
        Ship 2: C39 (buy COPPER_ORE) → B7 (sell) ✓ (no conflict)
    """

    def __init__(
        self,
        api: APIClient,
        db,
        player_id: int,
        logger: Optional[logging.Logger] = None,
    ):
        self.api = api
        self.db = db
        self.player_id = player_id
        self.logger = logger or logging.getLogger(__name__)

    def optimize_fleet(
        self,
        ships: List[Dict],
        system: str,
        max_stops: int,
        starting_credits: int,
    ) -> Optional[Dict]:
        """
        Find conflict-free routes for multiple ships

        Args:
            ships: List of ship data dicts with symbol, cargo capacity, fuel, etc.
            system: System symbol (e.g., "X1-TX46")
            max_stops: Maximum stops per route
            starting_credits: Available credits for purchases

        Returns:
            Dict with:
            - ship_routes: {ship_symbol: MultiLegRoute}
            - total_fleet_profit: Sum of all ship profits
            - conflicts: Number of conflicts avoided
        """
        self.logger.info("="*70)
        self.logger.info("FLEET TRADE ROUTE OPTIMIZATION")
        self.logger.info("="*70)
        self.logger.info(f"Ships: {len(ships)}")
        self.logger.info(f"System: {system}")
        self.logger.info(f"Max stops per route: {max_stops}")
        self.logger.info("="*70)

        # Track (resource, waypoint) BUY pairs across all assigned routes
        # This prevents ships from buying same resource at same waypoint
        reserved_resource_waypoints: set[tuple[str, str]] = set()

        # Store results
        ship_routes: Dict[str, MultiLegRoute] = {}

        # Process ships sequentially (greedy assignment)
        for i, ship_data in enumerate(ships, 1):
            ship_symbol = ship_data['symbol']
            self.logger.info(f"\n--- Optimizing Ship {i}/{len(ships)}: {ship_symbol} ---")

            # Get ship parameters
            start_waypoint = ship_data['nav']['waypointSymbol']
            cargo_capacity = ship_data['cargo']['capacity']
            ship_speed = ship_data['engine']['speed']
            fuel_capacity = ship_data['fuel']['capacity']
            current_fuel = ship_data['fuel']['current']

            # CRITICAL FIX: Extract actual ship cargo (may have residual from previous operations)
            starting_cargo = {item['symbol']: item['units']
                             for item in ship_data['cargo']['inventory']}

            # Create single-ship optimizer
            optimizer = MultiLegTradeOptimizer(
                api=self.api,
                db=self.db,
                player_id=self.player_id,
                logger=self.logger,
            )

            # Get all markets in system
            markets = optimizer._get_markets_in_system(system)

            # Get ALL trade opportunities (before filtering)
            all_opportunities = optimizer._get_trade_opportunities(system, markets)
            self.logger.info(f"  Total opportunities available: {len(all_opportunities)}")

            # CRITICAL: Filter out opportunities that would cause conflicts
            filtered_opportunities = self._filter_conflicting_opportunities(
                all_opportunities,
                reserved_resource_waypoints
            )

            conflicts_avoided = len(all_opportunities) - len(filtered_opportunities)
            self.logger.info(f"  Conflicts avoided: {conflicts_avoided}")
            self.logger.info(f"  Remaining opportunities: {len(filtered_opportunities)}")

            if not filtered_opportunities:
                self.logger.warning(f"  No conflict-free opportunities for {ship_symbol}")
                continue

            # Find best route using filtered opportunities
            route = self._find_ship_route(
                start_waypoint=start_waypoint,
                markets=markets,
                trade_opportunities=filtered_opportunities,
                max_stops=max_stops,
                cargo_capacity=cargo_capacity,
                starting_credits=starting_credits,
                ship_speed=ship_speed,
                starting_cargo=starting_cargo,
            )

            if not route:
                self.logger.warning(f"  No profitable route found for {ship_symbol}")
                continue

            if route.total_profit <= 0:
                self.logger.warning(f"  Route unprofitable for {ship_symbol}: {route.total_profit:,} cr")
                continue

            # SUCCESS: Assign route to ship
            ship_routes[ship_symbol] = route

            # Reserve (resource, waypoint) BUY pairs from this route
            new_reservations = self._extract_buy_pairs(route)
            self.logger.info(f"  ✅ Route assigned: {route.total_profit:,} cr profit")
            self.logger.info(f"  Reserved {len(new_reservations)} (resource, waypoint) pairs:")
            for good, waypoint in new_reservations:
                self.logger.info(f"     - {good} @ {waypoint}")
                reserved_resource_waypoints.add((good, waypoint))

        if not ship_routes:
            self.logger.error("No profitable conflict-free routes found for any ship")
            return None

        # Calculate fleet totals
        total_fleet_profit = sum(route.total_profit for route in ship_routes.values())

        self.logger.info("\n" + "="*70)
        self.logger.info("FLEET OPTIMIZATION COMPLETE")
        self.logger.info("="*70)
        self.logger.info(f"Ships with routes: {len(ship_routes)}/{len(ships)}")
        self.logger.info(f"Total fleet profit: {total_fleet_profit:,} cr")
        self.logger.info(f"Reserved (resource, waypoint) pairs: {len(reserved_resource_waypoints)}")
        self.logger.info("="*70)

        return {
            'ship_routes': ship_routes,
            'total_fleet_profit': total_fleet_profit,
            'reserved_pairs': reserved_resource_waypoints,
            'conflicts': 0,  # Conflicts were avoided, not detected after assignment
        }

    def _filter_conflicting_opportunities(
        self,
        opportunities: List[Dict],
        reserved_pairs: set[tuple[str, str]]
    ) -> List[Dict]:
        """
        Filter trade opportunities to remove those that would cause conflicts

        Args:
            opportunities: List of trade opportunity dicts
            reserved_pairs: Set of (resource, waypoint) BUY pairs already assigned

        Returns:
            Filtered list with conflicting opportunities removed
        """
        filtered = []

        for opp in opportunities:
            # Check if this opportunity's BUY location conflicts
            buy_pair = (opp['good'], opp['buy_waypoint'])

            if buy_pair in reserved_pairs:
                # CONFLICT: Another ship already buys this resource at this waypoint
                continue

            # No conflict - include this opportunity
            filtered.append(opp)

        return filtered

    def _extract_buy_pairs(self, route: MultiLegRoute) -> set[tuple[str, str]]:
        """
        Extract all (resource, waypoint) BUY pairs from a route

        Args:
            route: MultiLegRoute to analyze

        Returns:
            Set of (good, waypoint) tuples representing BUY actions
        """
        buy_pairs = set()

        for segment in route.segments:
            for action in segment.actions_at_destination:
                if action.action == 'BUY':
                    pair = (action.good, action.waypoint)
                    buy_pairs.add(pair)

        return buy_pairs

    def _find_ship_route(
        self,
        start_waypoint: str,
        markets: List[str],
        trade_opportunities: List[Dict],
        max_stops: int,
        cargo_capacity: int,
        starting_credits: int,
        ship_speed: int,
        starting_cargo: Optional[Dict[str, int]] = None,
    ) -> Optional[MultiLegRoute]:
        """
        Find optimal route for a single ship using greedy planner

        Args:
            start_waypoint: Ship's current location
            markets: Available markets
            trade_opportunities: Filtered trade opportunities (conflicts removed)
            max_stops: Maximum route stops
            cargo_capacity: Ship cargo capacity
            starting_credits: Available credits
            ship_speed: Ship speed for time estimation
            starting_cargo: Existing cargo from previous operations (residual)

        Returns:
            MultiLegRoute if found, None otherwise
        """
        from spacetraders_bot.operations.multileg_trader import ProfitFirstStrategy, GreedyRoutePlanner

        strategy = ProfitFirstStrategy(self.logger)
        planner = GreedyRoutePlanner(self.logger, self.db, strategy=strategy)

        route = planner.find_route(
            start_waypoint=start_waypoint,
            markets=markets,
            trade_opportunities=trade_opportunities,
            max_stops=max_stops,
            cargo_capacity=cargo_capacity,
            starting_credits=starting_credits,
            ship_speed=ship_speed,
            starting_cargo=starting_cargo,
        )

        return route


# ============================================================================
# CIRCUIT BREAKER SMART SKIP - Dependency Analysis & Intelligent Segment Independence
# ============================================================================

def analyze_route_dependencies(route: MultiLegRoute) -> Dict[int, SegmentDependency]:
    """
    Analyze route and build dependency graph for smart skip logic

    Properly tracks cargo flow by simulating cargo state at each segment,
    accounting for carry-through cargo and partial sells.

    Key insight: A segment depends on the ORIGINAL SOURCE of cargo, not just
    the most recent segment that touched that good.

    Returns:
        Map of segment_index → SegmentDependency
    """
    dependencies = {}

    # Track cargo state as we progress through route
    # Maps good -> {segment_index: units} to track WHERE each unit originated
    cargo_sources: Dict[str, Dict[int, int]] = {}

    for i, segment in enumerate(route.segments):
        dep = SegmentDependency(
            segment_index=i,
            depends_on=[],
            dependency_type='NONE',
            required_cargo={},
            required_credits=0,
            can_skip=True
        )

        # Check CARGO dependencies: Do we need to SELL goods from prior segments?
        sell_actions = [a for a in segment.actions_at_destination if a.action == 'SELL']
        for sell_action in sell_actions:
            good = sell_action.good
            units_to_sell = sell_action.units

            # Find ALL source segments that contributed to this cargo
            if good in cargo_sources:
                remaining_to_sell = units_to_sell
                # Consume cargo from sources in order (FIFO-like)
                for source_idx in sorted(cargo_sources[good].keys()):
                    available_from_source = cargo_sources[good][source_idx]
                    units_consumed = min(remaining_to_sell, available_from_source)

                    if units_consumed > 0:
                        # This segment depends on source_idx
                        if source_idx not in dep.depends_on:
                            dep.depends_on.append(source_idx)
                        dep.dependency_type = 'CARGO'
                        dep.required_cargo[good] = dep.required_cargo.get(good, 0) + units_consumed
                        dep.can_skip = False

                        remaining_to_sell -= units_consumed
                        if remaining_to_sell == 0:
                            break

        # Update cargo sources: Process SELL actions (remove cargo)
        for sell_action in sell_actions:
            good = sell_action.good
            units_to_sell = sell_action.units

            if good in cargo_sources:
                remaining_to_sell = units_to_sell
                # Remove sold cargo from sources (FIFO-like)
                for source_idx in sorted(list(cargo_sources[good].keys())):
                    available = cargo_sources[good][source_idx]
                    consumed = min(remaining_to_sell, available)

                    cargo_sources[good][source_idx] -= consumed
                    remaining_to_sell -= consumed

                    # Clean up depleted sources
                    if cargo_sources[good][source_idx] == 0:
                        del cargo_sources[good][source_idx]

                    if remaining_to_sell == 0:
                        break

                # Clean up empty goods
                if not cargo_sources[good]:
                    del cargo_sources[good]

        # Update cargo sources: Process BUY actions (add cargo)
        buy_actions = [a for a in segment.actions_at_destination if a.action == 'BUY']
        for buy_action in buy_actions:
            good = buy_action.good
            units = buy_action.units

            # Add this segment as a source for this good
            if good not in cargo_sources:
                cargo_sources[good] = {}
            cargo_sources[good][i] = cargo_sources[good].get(i, 0) + units

        # Check CREDIT dependencies: Do we need revenue to afford purchases?
        total_buy_cost = sum(a.total_value for a in buy_actions)

        if total_buy_cost > 0:
            dep.required_credits = total_buy_cost

        dependencies[i] = dep

    return dependencies


def should_skip_segment(
    segment_index: int,
    failure_reason: str,
    dependencies: Dict[int, SegmentDependency],
    route: MultiLegRoute,
    current_cargo: Dict[str, int],
    current_credits: int
) -> Tuple[bool, str]:
    """
    Determine if failed segment should be skipped vs abort operation

    Returns:
        (should_skip, reason)
    """
    dep = dependencies[segment_index]

    # Build transitive closure of dependencies: segments affected by this failure
    affected_segments = {segment_index}  # Start with the failed segment itself

    # Keep expanding until no new dependencies found (transitive closure)
    changed = True
    while changed:
        changed = False
        for seg_idx in range(segment_index + 1, len(route.segments)):
            if seg_idx not in affected_segments:
                seg_dep = dependencies[seg_idx]
                # Check if this segment depends on any affected segment
                if any(dep_idx in affected_segments for dep_idx in seg_dep.depends_on):
                    affected_segments.add(seg_idx)
                    changed = True

    # Check if any remaining segments are independent
    remaining_segments = route.segments[segment_index + 1:]
    independent_segments = []

    for i, remaining_seg in enumerate(remaining_segments):
        remaining_idx = segment_index + 1 + i
        if remaining_idx not in affected_segments:
            independent_segments.append((remaining_idx, remaining_seg))

    # IMPORTANT: Check dependency first before profit
    # If all segments depend on failed segment, abort regardless of profit calculation
    if len(independent_segments) == 0:
        return False, "All remaining segments depend on failed segment - must abort"

    # Check if remaining independent segments are profitable enough
    # Calculate incremental profit for each independent segment
    remaining_profit = 0
    for seg_idx, seg in independent_segments:
        # Calculate this segment's profit contribution
        if seg_idx > 0:
            prior_seg = route.segments[seg_idx - 1]
            segment_profit = seg.cumulative_profit - prior_seg.cumulative_profit
        else:
            segment_profit = seg.cumulative_profit

        remaining_profit += segment_profit

    if remaining_profit < 5000:  # Configurable minimum threshold
        return False, f"Remaining profit too low ({remaining_profit:,} credits < 5,000 minimum)"

    return True, f"Can skip - {len(independent_segments)} independent segments remain with {remaining_profit:,} profit"


def cargo_blocks_future_segments(
    cargo: Dict[str, int],
    remaining_segments: List[RouteSegment],
    ship_capacity: int
) -> bool:
    """
    Check if current cargo prevents future segments from executing

    Returns:
        True if cargo blocks any segment's buy actions
    """
    cargo_used = sum(cargo.values())
    cargo_available = ship_capacity - cargo_used

    for segment in remaining_segments:
        buy_actions = [a for a in segment.actions_at_destination if a.action == 'BUY']
        units_needed = sum(a.units for a in buy_actions)

        if units_needed > cargo_available:
            return True

    return False


def find_best_nearby_market(
    db,
    good: str,
    system: str,
    max_distance: int = 100,
    updated_within_hours: float = 2.0
) -> Optional[Dict]:
    """
    Find best buyer market for a good within distance threshold

    Args:
        db: Database instance
        good: Trade good symbol
        system: System symbol
        max_distance: Maximum distance in units
        updated_within_hours: Data freshness threshold

    Returns:
        Dict with waypoint, sellPrice, distance or None if no good market found
    """
    # Query database for best buyers
    # This is simplified - actual implementation would use db.find_best_buyer with distance filter
    # For now, return None to indicate no nearby market found
    return None


def calculate_salvage_metrics(
    good: str,
    units: int,
    current_waypoint: str,
    best_waypoint: str,
    current_price: int,
    best_price: int,
    ship_speed: int,
    remaining_segment_value: int,
    db
) -> Dict:
    """
    Calculate salvage decision metrics for tiered salvage system

    Returns:
        Dict with distance, deviation_time_min, salvage_gain, opportunity_cost
    """
    # Calculate distance (simplified - would use db waypoint coordinates)
    # For now, return placeholder values
    distance = 100  # units
    deviation_time_min = (distance / ship_speed) * 60 / 3600  # Convert to minutes
    salvage_gain = (best_price - current_price) * units

    # Opportunity cost: time spent deviating vs value of remaining segments
    opportunity_cost = (deviation_time_min / 60) * (remaining_segment_value / 2)  # Rough estimate

    return {
        'distance': distance,
        'deviation_time_min': deviation_time_min,
        'salvage_gain': salvage_gain,
        'opportunity_cost': opportunity_cost
    }


def skip_segment_and_cleanup_tiered(
    segment: RouteSegment,
    ship: ShipController,
    api: APIClient,
    db,
    navigator: SmartNavigator,
    logger: logging.Logger,
    remaining_segments: List[RouteSegment],
    ship_capacity: int
) -> Tuple[bool, int]:
    """
    Skip failed segment and intelligently salvage cargo using tiered strategy

    CRITICAL: Never sell at circuit breaker market - that's the worst price!

    Four-Tier Salvage System:
    - Tier 1: Emergency salvage at current market (fastest, stay on route)
    - Tier 2: Deviate to adjacent market (<100u, gain >50k)
    - Tier 3: Hold cargo for opportunistic sale at future markets
    - Tier 4: End-of-route best market navigation

    Args:
        segment: The failed segment to skip
        ship: ShipController instance
        api: APIClient instance
        db: Database instance
        navigator: SmartNavigator instance
        logger: Logger instance
        remaining_segments: List of remaining route segments after this one
        ship_capacity: Ship cargo capacity

    Returns:
        (success, credits_recovered)
    """
    logger.warning("="*70)
    logger.warning(f"⚠️  SKIPPING SEGMENT: {segment.from_waypoint} → {segment.to_waypoint}")
    logger.warning("="*70)

    # Get current cargo and location
    ship_data = ship.get_status()
    if not ship_data:
        logger.error("Failed to get ship status for cargo cleanup")
        return False, 0

    cargo = ship_data.get('cargo', {}).get('inventory', [])
    current_waypoint = ship_data['nav']['waypointSymbol']
    system = ship_data['nav']['systemSymbol']

    # Identify cargo specific to this failed segment (goods planned to BUY in this segment)
    segment_goods = {action.good for action in segment.actions_at_destination if action.action == 'BUY'}

    stranded_cargo = []
    for item in cargo:
        if item['symbol'] in segment_goods:
            stranded_cargo.append(item)

    if not stranded_cargo:
        logger.warning("No stranded cargo from failed segment - continuing with remaining segments")
        return True, 0

    logger.warning(f"Stranded cargo from failed segment:")
    for item in stranded_cargo:
        logger.warning(f"  - {item['units']}x {item['symbol']}")

    total_recovered = 0

    # Check if cargo blocks future segments
    cargo_dict = {item['symbol']: item['units'] for item in cargo}
    blocks_future = cargo_blocks_future_segments(cargo_dict, remaining_segments, ship_capacity)

    # Calculate remaining route value for opportunity cost analysis
    remaining_value = sum(seg.cumulative_profit for seg in remaining_segments) if remaining_segments else 0

    for item in stranded_cargo:
        good = item['symbol']
        units = item['units']

        # TIER 1: Emergency salvage at current market (fastest, accept any price)
        # When: Cargo blocks future segments OR high opportunity cost OR no better option
        if blocks_future or remaining_value > 100000:
            logger.warning(f"⚡ TIER 1: Emergency salvage at current market")
            reason = "Cargo blocking future segments" if blocks_future else "High opportunity cost"
            logger.warning(f"   Reason: {reason}")
            logger.warning(f"   Accepting current market price (route integrity > salvage optimization)")

            try:
                # Ensure ship is docked
                if ship_data['nav']['status'] != 'DOCKED':
                    ship.dock()

                result = ship.sell(good, units, check_market_prices=False)
                if result:
                    total_recovered += result['totalPrice']
                    logger.warning(f"  ✅ Salvaged {units}x {good} for {result['totalPrice']:,} credits")
                else:
                    logger.error(f"  ❌ Failed to salvage {good}")
            except Exception as e:
                logger.error(f"  ❌ Failed to salvage {good}: {e}")

        # TIER 3: Hold for opportunistic sale (cargo doesn't block, route has more markets)
        elif len(remaining_segments) > 0 and not blocks_future:
            logger.warning(f"📦 TIER 3: Holding {units}x {good} for opportunistic sale at future markets")
            logger.warning(f"   Will check prices at {len(remaining_segments)} remaining markets")
            logger.warning(f"   No immediate salvage - continuing with cargo")
            # Don't sell - cargo will be checked at each future market
            # This requires integration into execute_multileg_route to check held cargo prices

        # TIER 4: End of route - sell at current market (no more segments)
        else:
            logger.warning(f"🏁 TIER 4: Route near end, selling at current market")
            logger.warning(f"   No future segments to optimize for")

            try:
                if ship_data['nav']['status'] != 'DOCKED':
                    ship.dock()

                result = ship.sell(good, units, check_market_prices=False)
                if result:
                    total_recovered += result['totalPrice']
                    logger.warning(f"  ✅ Salvaged {units}x {good} for {result['totalPrice']:,} credits")
            except Exception as e:
                logger.error(f"  ❌ Failed to salvage {good}: {e}")

    logger.warning(f"Total salvage recovered: {total_recovered:,} credits")
    logger.warning("Continuing with remaining independent segments...")
    logger.warning("="*70)

    return True, total_recovered


def execute_multileg_route(
    route: MultiLegRoute,
    ship: ShipController,
    api: APIClient,
    db,
    player_id: int
) -> bool:
    """
    Execute a multi-leg trading route with live monitoring and circuit breakers

    Args:
        route: The planned multi-leg trading route
        ship: ShipController instance for the executing ship
        api: APIClient instance
        db: Database instance for market data
        player_id: Player ID for database queries

    Returns:
        True if route executed successfully, False on any critical failure
    """
    logging.info("\n" + "="*70)
    logging.info("ROUTE EXECUTION START")
    logging.info("="*70)

    # Get ship data to determine system
    ship_data = ship.get_status()
    if not ship_data:
        logging.error("Failed to get ship status")
        return False

    system = ship_data['nav']['systemSymbol']

    # Create SmartNavigator for intelligent routing
    navigator = SmartNavigator(api, system)

    # Track cumulative metrics
    total_revenue = 0
    total_costs = 0
    starting_location = ship_data['nav']['waypointSymbol']

    # Get starting credits
    agent = api.get_agent()
    if not agent:
        logging.error("Failed to get agent data")
        return False
    starting_credits = agent['credits']

    logging.info(f"Starting location: {starting_location}")
    logging.info(f"Starting credits: {starting_credits:,}")
    logging.info(f"Route segments: {len(route.segments)}")
    logging.info("="*70)

    # PRE-FLIGHT VALIDATION: Check market data freshness for all route waypoints
    logging.info("\n" + "="*70)
    logging.info("PRE-FLIGHT MARKET DATA VALIDATION")
    logging.info("="*70)

    stale_markets = []
    aging_markets = []

    with db.connection() as conn:
        for segment in route.segments:
            for action in segment.actions_at_destination:
                waypoint = action.waypoint
                good = action.good

                # Get market data from database
                market_data = db.get_market_data(conn, waypoint, good)
                if not market_data or len(market_data) == 0:
                    logging.warning(f"⚠️  No market data found for {waypoint} {good}")
                    continue

                # Check data age
                last_updated = market_data[0].get('last_updated')
                if not last_updated:
                    logging.warning(f"⚠️  No timestamp for {waypoint} {good} (skipping freshness check)")
                    continue

                try:
                    timestamp = datetime.strptime(last_updated, '%Y-%m-%dT%H:%M:%S.%fZ').replace(tzinfo=timezone.utc)
                    age_hours = (datetime.now(timezone.utc) - timestamp).total_seconds() / 3600

                    # Classify by freshness
                    if age_hours > 1.0:
                        stale_markets.append((waypoint, good, age_hours))
                        logging.error(f"  ❌ STALE: {waypoint} {good} ({age_hours:.1f}h old)")
                    elif age_hours > 0.5:
                        aging_markets.append((waypoint, good, age_hours))
                        logging.warning(f"  ⚠️  AGING: {waypoint} {good} ({age_hours:.1f}h old)")
                    else:
                        logging.info(f"  ✅ FRESH: {waypoint} {good} ({age_hours*60:.0f}min old)")
                except (ValueError, TypeError) as e:
                    logging.warning(f"  ⚠️  Invalid timestamp for {waypoint} {good}: {e}")

    # Abort if any market data is stale (>1 hour)
    if stale_markets:
        logging.error("\n" + "="*70)
        logging.error("🚨 PRE-FLIGHT VALIDATION FAILED: STALE MARKET DATA")
        logging.error("="*70)
        logging.error(f"Found {len(stale_markets)} markets with stale data (>1 hour old):")
        for waypoint, good, age_hours in stale_markets:
            logging.error(f"  - {waypoint} {good}: {age_hours:.1f}h old")
        logging.error("")
        logging.error("RECOMMENDATION: Wait for scout fleet to refresh market data, or manually re-scout these markets")
        logging.error("🛑 Route execution ABORTED to prevent trading with stale prices")
        logging.error("="*70)
        return False

    # Warn if any market data is aging (30min-1hr)
    if aging_markets:
        logging.warning("\n" + "⏰ WARNING: Some market data is aging (30min-1hr old):")
        for waypoint, good, age_hours in aging_markets:
            logging.warning(f"  - {waypoint} {good}: {age_hours:.1f}h old")
        logging.warning("  Proceeding with caution - prices may have shifted")
        logging.warning("")

    if not stale_markets and not aging_markets:
        logging.info("✅ All market data is fresh (<30 minutes old)")

    logging.info("="*70)

    # NEW: Analyze dependencies BEFORE execution for smart skip logic
    dependencies = analyze_route_dependencies(route)

    logging.info("\n" + "="*70)
    logging.info("DEPENDENCY ANALYSIS")
    logging.info("="*70)
    for idx, dep in dependencies.items():
        dep_type_str = dep.dependency_type if dep.dependency_type != 'NONE' else 'INDEPENDENT'
        logging.info(f"Segment {idx}: {dep_type_str}, depends_on={dep.depends_on}, can_skip={dep.can_skip}")
    logging.info("="*70)

    # Track skipped segments for smart skip logic
    skipped_segments = set()

    # Execute each segment
    for segment_num, segment in enumerate(route.segments, 1):
        segment_index = segment_num - 1

        # Skip if dependent on a failed segment (transitive dependency check)
        if any(dep_idx in skipped_segments for dep_idx in dependencies[segment_index].depends_on):
            logging.warning("="*70)
            logging.warning(f"⏭️  SKIPPING SEGMENT {segment_num} - depends on failed segment")
            logging.warning("="*70)
            skipped_segments.add(segment_index)
            continue
        logging.info("\n" + "-"*70)
        logging.info(f"SEGMENT {segment_num}/{len(route.segments)}: {segment.from_waypoint} → {segment.to_waypoint}")
        logging.info("-"*70)
        logging.info(f"Distance: {segment.distance:.0f} units")
        logging.info(f"Fuel cost estimate: {segment.fuel_cost}")
        logging.info(f"Actions planned: {len(segment.actions_at_destination)}")

        try:
            # Step 1: Navigate to destination waypoint
            logging.info(f"\n🚀 Navigating to {segment.to_waypoint}...")

            if not navigator.execute_route(ship, segment.to_waypoint):
                logging.error(f"❌ Navigation failed to {segment.to_waypoint}")
                logging.error("Route execution aborted")
                _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index)
                return False

            logging.info(f"✅ Arrived at {segment.to_waypoint}")

            # Step 2: Dock at waypoint for trading
            logging.info(f"\n🛬 Docking at {segment.to_waypoint}...")
            if not ship.dock():
                logging.error("❌ Failed to dock")
                logging.error("Route execution aborted")
                _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index)
                return False

            # Step 3: Execute trade actions at this waypoint
            logging.info(f"\n💼 Executing {len(segment.actions_at_destination)} trade actions...")

            segment_revenue = 0
            segment_costs = 0

            for action_num, action in enumerate(segment.actions_at_destination, 1):
                logging.info(f"\n  Action {action_num}/{len(segment.actions_at_destination)}: {action.action} {action.units}x {action.good}")

                if action.action == 'BUY':
                    # Purchase cargo with batch purchasing and inter-batch price validation
                    logging.info(f"  💰 Buying {action.units}x {action.good} @ {action.price_per_unit:,} = {action.total_value:,} credits")

                    # Determine batch size dynamically based on good value
                    def calculate_batch_size(price_per_unit: int) -> int:
                        """
                        Calculate optimal batch size based on good value

                        Higher value goods = smaller batches (detect spikes earlier, minimize risk)
                        Lower value goods = larger batches (reduce API overhead)

                        Args:
                            price_per_unit: Price per unit in credits

                        Returns:
                            Optimal batch size (2-10 units)
                        """
                        if price_per_unit >= 2000:
                            return 2  # High-value: 2-unit batches (minimal risk, e.g. CLOTHING @2892)
                        elif price_per_unit >= 1500:
                            return 3  # Medium-high value: 3-unit batches (e.g. GOLD @1500)
                        elif price_per_unit >= 50:
                            return 5  # Standard: 5-unit batches (default for most goods)
                        else:
                            return 10  # Very low-value: 10-unit batches (bulk efficiency)

                    batch_size = calculate_batch_size(action.price_per_unit)
                    total_units_to_buy = action.units
                    units_remaining = total_units_to_buy

                    # Log batch sizing decision with rationale
                    if action.price_per_unit >= 2000:
                        logging.info(f"  📊 Dynamic batch size: {batch_size} units (high-value good ≥2000 cr/unit - minimal risk strategy)")
                    elif action.price_per_unit >= 1500:
                        logging.info(f"  📊 Dynamic batch size: {batch_size} units (medium-high value ≥1500 cr/unit - cautious approach)")
                    elif action.price_per_unit >= 50:
                        logging.info(f"  📊 Dynamic batch size: {batch_size} units (standard good ≥50 cr/unit - default batching)")
                    else:
                        logging.info(f"  📊 Dynamic batch size: {batch_size} units (bulk good <50 cr/unit - efficiency mode)")

                    # If purchase is smaller than batch size, use single transaction
                    if total_units_to_buy <= batch_size:
                        logging.info(f"  📦 Small purchase ({total_units_to_buy} units) - using single transaction")

                        # CRITICAL FIX: Get fresh market data BEFORE purchase and abort if price spiked
                        try:
                            live_market = api.get_market(system, action.waypoint)
                            if live_market:
                                # Check current buy price
                                live_buy_price = None
                                trade_volume = None
                                for good in live_market.get('tradeGoods', []):
                                    if good['symbol'] == action.good:
                                        live_buy_price = good.get('sellPrice')  # What we pay
                                        trade_volume = good.get('tradeVolume')
                                        break

                                if live_buy_price:
                                    price_change_pct = ((live_buy_price - action.price_per_unit) / action.price_per_unit) * 100 if action.price_per_unit > 0 else 0

                                    if abs(price_change_pct) > 5:
                                        logging.warning(f"  ⚠️  Price changed: {action.price_per_unit:,} → {live_buy_price:,} ({price_change_pct:+.1f}%)")

                                    # Log volatility warning if price spiked significantly
                                    if price_change_pct > 30:
                                        logging.warning("="*70)
                                        logging.warning("⚠️  HIGH PRICE VOLATILITY DETECTED")
                                        logging.warning("="*70)
                                        logging.warning(f"  Planned: {action.price_per_unit:,} cr/unit")
                                        logging.warning(f"  Current: {live_buy_price:,} cr/unit")
                                        logging.warning(f"  Increase: {price_change_pct:.1f}%")
                                        logging.warning(f"  Likely cause: Stale market cache or market shift")
                                        logging.warning(f"  Checking profitability before proceeding...")
                                        logging.warning("="*70)

                                    # CIRCUIT BREAKER: Check profitability using planned sell price
                                    planned_sell_price = _find_planned_sell_price(action.good, route, segment_index)
                                    if planned_sell_price:
                                        # Apply degradation model to expected sell price
                                        expected_sell_price = estimate_sell_price_with_degradation(planned_sell_price, total_units_to_buy)

                                        # Check if purchase would be unprofitable
                                        if live_buy_price >= expected_sell_price:
                                            logging.error("="*70)
                                            logging.error("🚨 CIRCUIT BREAKER: UNPROFITABLE TRADE DETECTED!")
                                            logging.error("="*70)
                                            logging.error(f"  Good: {action.good}")
                                            logging.error(f"  Live buy price: {live_buy_price:,} cr/unit")
                                            logging.error(f"  Planned sell price (cached): {planned_sell_price:,} cr/unit")
                                            logging.error(f"  Expected sell price (w/ degradation): {expected_sell_price:,} cr/unit")
                                            if total_units_to_buy > 20:
                                                degradation_pct = ((planned_sell_price - expected_sell_price) / planned_sell_price) * 100
                                                logging.error(f"  Price degradation: -{degradation_pct:.1f}% ({total_units_to_buy} units)")
                                            logging.error(f"  Would lose: {live_buy_price - expected_sell_price:,} cr/unit")
                                            logging.error(f"  🛡️  PURCHASE BLOCKED - No credits spent")
                                            logging.error("="*70)

                                            # Smart skip decision: Can we skip this segment and continue?
                                            current_ship_data = ship.get_status()
                                            current_cargo = {item['symbol']: item['units'] for item in current_ship_data['cargo']['inventory']}
                                            current_agent = api.get_agent()
                                            current_credits = current_agent['credits'] if current_agent else 0

                                            should_skip, reason = should_skip_segment(
                                                segment_index=segment_index,
                                                failure_reason="Unprofitable trade (buy >= sell)",
                                                dependencies=dependencies,
                                                route=route,
                                                current_cargo=current_cargo,
                                                current_credits=current_credits
                                            )

                                            if should_skip:
                                                logging.warning(f"Smart skip decision: {reason}")
                                                remaining_segments = route.segments[segment_index + 1:]
                                                skip_segment_and_cleanup_tiered(
                                                    segment=segment,
                                                    ship=ship,
                                                    api=api,
                                                    db=db,
                                                    navigator=navigator,
                                                    logger=logging.getLogger(__name__),
                                                    remaining_segments=remaining_segments,
                                                    ship_capacity=ship_data['cargo']['capacity']
                                                )
                                                skipped_segments.add(segment_index)
                                                break  # Exit action loop, continue to next segment
                                            else:
                                                logging.error(f"Cannot skip: {reason}")
                                                logging.error("  Route execution aborted to prevent loss")
                                                _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index)
                                                return False
                                        else:
                                            # Profitability check passed - allow trade
                                            profit_margin = expected_sell_price - live_buy_price
                                            profit_margin_pct = (profit_margin / expected_sell_price) * 100
                                            if price_change_pct > 30:
                                                logging.info(f"  ✅ Trade still profitable despite price spike: +{profit_margin:,} cr/unit ({profit_margin_pct:.1f}% margin)")
                                            else:
                                                logging.info(f"  ✅ Trade profitable: +{profit_margin:,} cr/unit ({profit_margin_pct:.1f}% margin)")
                                    else:
                                        # No planned sell price - can't validate profitability
                                        if price_change_pct > 50:
                                            # Extreme volatility with no profitability validation - abort conservatively
                                            logging.error("="*70)
                                            logging.error("🚨 CIRCUIT BREAKER: EXTREME VOLATILITY + NO SELL PRICE!")
                                            logging.error("="*70)
                                            logging.error(f"  Price spike: {price_change_pct:.1f}%")
                                            logging.error(f"  No planned sell action found for {action.good}")
                                            logging.error(f"  Cannot validate trade profitability")
                                            logging.error(f"  🛡️  PURCHASE BLOCKED - Too risky without sell price")
                                            logging.error("="*70)
                                            _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index)
                                            return False
                                else:
                                    # No live price data - this is risky, abort to be safe
                                    logging.error("="*70)
                                    logging.error("🚨 CIRCUIT BREAKER: NO LIVE PRICE DATA!")
                                    logging.error("="*70)
                                    logging.error(f"  Unable to verify current price for {action.good}")
                                    logging.error(f"  Expected: {action.price_per_unit:,} cr/unit")
                                    logging.error(f"  Current: UNKNOWN")
                                    logging.error(f"  🛡️  PURCHASE BLOCKED - Cannot validate price safety")
                                    logging.error("="*70)
                                    logging.error("  Route execution aborted to prevent unsafe purchase")
                                    _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index)
                                    return False
                            else:
                                # Market data fetch returned None - abort to be safe
                                logging.error("="*70)
                                logging.error("🚨 CIRCUIT BREAKER: MARKET DATA UNAVAILABLE!")
                                logging.error("="*70)
                                logging.error(f"  Unable to fetch market data for {action.waypoint}")
                                logging.error(f"  🛡️  PURCHASE BLOCKED - Cannot validate price safety")
                                logging.error("="*70)
                                logging.error("  Route execution aborted to prevent unsafe purchase")
                                _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index)
                                return False
                        except Exception as e:
                            # Market API call failed - abort to be safe
                            logging.error("="*70)
                            logging.error("🚨 CIRCUIT BREAKER: MARKET API FAILURE!")
                            logging.error("="*70)
                            logging.error(f"  Error: {e}")
                            logging.error(f"  Unable to verify price for {action.good} at {action.waypoint}")
                            logging.error(f"  🛡️  PURCHASE BLOCKED - Cannot validate price safety")
                            logging.error("="*70)
                            logging.error("  Route execution aborted to prevent unsafe purchase")
                            import traceback
                            logging.error(traceback.format_exc())
                            _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index)
                            return False

                        # CARGO OVERFLOW FIX: Check current cargo space before purchase
                        # The route planner assumes cargo state, but execution may diverge
                        # (skipped segments, failed sells, residual cargo, etc.)
                        current_ship_data = ship.get_status()
                        current_cargo_units = sum(item['units'] for item in current_ship_data['cargo']['inventory'])
                        cargo_available = current_ship_data['cargo']['capacity'] - current_cargo_units

                        if cargo_available <= 0:
                            logging.error("="*70)
                            logging.error("🚨 CARGO OVERFLOW PREVENTION: CARGO FULL!")
                            logging.error("="*70)
                            logging.error(f"  Current cargo: {current_cargo_units}/{current_ship_data['cargo']['capacity']} units")
                            logging.error(f"  Planned purchase: {total_units_to_buy} units of {action.good}")
                            logging.error(f"  🛡️  PURCHASE BLOCKED - No cargo space available")
                            logging.error("="*70)
                            logging.error("  Route execution aborted to prevent API error")
                            _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index)
                            return False

                        if total_units_to_buy > cargo_available:
                            logging.warning("="*70)
                            logging.warning("⚠️  CARGO CAPACITY LIMIT: Reducing purchase quantity")
                            logging.warning("="*70)
                            logging.warning(f"  Current cargo: {current_cargo_units}/{current_ship_data['cargo']['capacity']} units")
                            logging.warning(f"  Available space: {cargo_available} units")
                            logging.warning(f"  Planned purchase: {total_units_to_buy} units")
                            logging.warning(f"  Adjusted purchase: {cargo_available} units (reduced to fit capacity)")
                            logging.warning("="*70)
                            total_units_to_buy = cargo_available

                        # Execute purchase (only reached if price validation passed)
                        transaction = ship.buy(action.good, total_units_to_buy)
                        if not transaction:
                            logging.error(f"  ❌ Purchase failed for {action.good}")
                            logging.error("  Route execution aborted")
                            _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index)
                            return False

                        actual_cost = transaction['totalPrice']
                        actual_price_per_unit = actual_cost / transaction['units'] if transaction['units'] > 0 else 0

                        # Update database with ACTUAL transaction price
                        _update_market_price_from_transaction(
                            db=db,
                            waypoint=action.waypoint,
                            good=action.good,
                            transaction_type='PURCHASE',
                            price_per_unit=int(actual_price_per_unit),
                            logger=logging.getLogger(__name__)
                        )

                        # CIRCUIT BREAKER: Check actual purchase price vs PLANNED SELL PRICE (not planned buy price!)
                        # CRITICAL FIX: Compare buy vs sell to validate profitability, not buy vs database cache
                        # DEGRADATION FIX: Account for expected price erosion during batch selling
                        planned_sell_price = _find_planned_sell_price(action.good, route, segment_index)

                        if planned_sell_price:
                            # Apply degradation model to expected sell price
                            # Large cargo quantities (>20 units) experience ~0.23% per-unit price erosion
                            units_to_sell = transaction['units']
                            expected_sell_price = estimate_sell_price_with_degradation(planned_sell_price, units_to_sell)

                            # Calculate actual profitability with degradation factored in
                            profit_per_unit = expected_sell_price - actual_price_per_unit
                            is_unprofitable = actual_price_per_unit >= expected_sell_price

                            if is_unprofitable:
                                logging.error("="*70)
                                logging.error("🚨 CIRCUIT BREAKER: UNPROFITABLE TRADE DETECTED!")
                                logging.error("="*70)
                                logging.error(f"  Good: {action.good}")
                                logging.error(f"  Actual buy price: {actual_price_per_unit:,.0f} cr/unit")
                                logging.error(f"  Planned sell price (cached): {planned_sell_price:,} cr/unit")
                                logging.error(f"  Expected sell price (w/ degradation): {expected_sell_price:,} cr/unit")
                                if units_to_sell > 20:
                                    degradation_pct = ((planned_sell_price - expected_sell_price) / planned_sell_price) * 100
                                    logging.error(f"  Price degradation: -{degradation_pct:.1f}% ({units_to_sell} units)")
                                logging.error(f"  Loss per unit: {profit_per_unit:,.0f} cr")
                                logging.error(f"  Total potential loss: {profit_per_unit * transaction['units']:,.0f} cr")
                                logging.error(f"  Already spent: {actual_cost:,} credits on this purchase")
                                logging.error("="*70)
                            elif abs(profit_per_unit) / expected_sell_price < 0.05:  # <5% margin
                                logging.warning("="*70)
                                logging.warning("⚠️  LOW PROFIT MARGIN WARNING")
                                logging.warning("="*70)
                                logging.warning(f"  Good: {action.good}")
                                logging.warning(f"  Actual buy price: {actual_price_per_unit:,.0f} cr/unit")
                                logging.warning(f"  Planned sell price (cached): {planned_sell_price:,} cr/unit")
                                logging.warning(f"  Expected sell price (w/ degradation): {expected_sell_price:,} cr/unit")
                                if units_to_sell > 20:
                                    degradation_pct = ((planned_sell_price - expected_sell_price) / planned_sell_price) * 100
                                    logging.warning(f"  Price degradation: -{degradation_pct:.1f}% ({units_to_sell} units)")
                                logging.warning(f"  Profit per unit: {profit_per_unit:,.0f} cr ({(profit_per_unit/expected_sell_price*100):.1f}%)")
                                logging.warning(f"  Continuing with thin margin...")
                                logging.warning("="*70)
                        else:
                            # No sell action found - log warning but use old logic as fallback
                            logging.warning(f"  ⚠️  No planned sell action found for {action.good} - using price spike detection")
                            if action.price_per_unit > 0:
                                actual_price_change_pct = ((actual_price_per_unit - action.price_per_unit) / action.price_per_unit) * 100

                                if actual_price_change_pct > 30:
                                    is_unprofitable = True
                                    logging.error("="*70)
                                    logging.error("🚨 CIRCUIT BREAKER: ACTUAL BUY PRICE SPIKE!")
                                    logging.error("="*70)
                                    logging.error(f"  Planned: {action.price_per_unit:,} cr/unit")
                                    logging.error(f"  Actual: {actual_price_per_unit:,.0f} cr/unit")
                                    logging.error(f"  Increase: {actual_price_change_pct:.1f}%")
                                    logging.error(f"  Already spent: {actual_cost:,} credits on this purchase")
                                    logging.error("="*70)
                                else:
                                    is_unprofitable = False
                            else:
                                is_unprofitable = False

                        # Only trigger salvage logic if trade is unprofitable
                        if is_unprofitable:
                            # Smart skip decision: Already bought at bad price, salvage and continue if possible
                            current_ship_data = ship.get_status()
                            current_cargo = {item['symbol']: item['units'] for item in current_ship_data['cargo']['inventory']}
                            current_agent = api.get_agent()
                            current_credits = current_agent['credits'] if current_agent else 0

                            should_skip, reason = should_skip_segment(
                                segment_index=segment_index,
                                failure_reason="Unprofitable trade (buy >= sell)",
                                dependencies=dependencies,
                                route=route,
                                current_cargo=current_cargo,
                                current_credits=current_credits
                            )

                            if should_skip:
                                logging.warning(f"Smart skip decision: {reason}")
                                remaining_segments = route.segments[segment_index + 1:]
                                skip_segment_and_cleanup_tiered(
                                    segment=segment,
                                    ship=ship,
                                    api=api,
                                    db=db,
                                    navigator=navigator,
                                    logger=logging.getLogger(__name__),
                                    remaining_segments=remaining_segments,
                                    ship_capacity=ship_data['cargo']['capacity']
                                )
                                skipped_segments.add(segment_index)
                                break  # Exit action loop, continue to next segment
                            else:
                                logging.error(f"Cannot skip: {reason}")
                                logging.error("  Aborting route to prevent further losses")
                                # CRITICAL FIX: Only salvage the unprofitable item (action.good)
                                # Keep other cargo for future profitable segments
                                _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index, unprofitable_item=action.good)
                                return False
                        elif abs(profit_per_unit) / planned_sell_price > 0.05 and action.price_per_unit > 0:
                            # Still profitable, but check if price changed significantly from planned buy price
                            actual_price_change_pct = ((actual_price_per_unit - action.price_per_unit) / action.price_per_unit) * 100
                            if abs(actual_price_change_pct) > 5:
                                logging.warning(f"  ⚠️  Actual price: {action.price_per_unit:,} → {actual_price_per_unit:,.0f} ({actual_price_change_pct:+.1f}%)")

                        segment_costs += actual_cost
                        total_costs += actual_cost

                        logging.info(f"  ✅ Purchased {transaction['units']}x {action.good} for {actual_cost:,} credits")

                    else:
                        # BATCH PURCHASING: Split large purchase into batches with inter-batch validation
                        num_batches = (total_units_to_buy + batch_size - 1) // batch_size
                        logging.info(f"  📦 Batch purchasing: {total_units_to_buy} units split into {num_batches} batches of {batch_size} units")

                        batch_num = 0
                        total_batch_cost = 0
                        total_units_purchased = 0
                        initial_batch_price = None
                        batch_aborted = False

                        while units_remaining > 0 and not batch_aborted:
                            batch_num += 1
                            units_this_batch = min(batch_size, units_remaining)

                            logging.info(f"    Batch {batch_num}/{num_batches}: Purchasing {units_this_batch} units...")

                            # INTER-BATCH VALIDATION: Check price BEFORE each batch
                            try:
                                live_market = api.get_market(system, action.waypoint)
                                if live_market:
                                    live_buy_price = None
                                    for good in live_market.get('tradeGoods', []):
                                        if good['symbol'] == action.good:
                                            live_buy_price = good.get('sellPrice')
                                            break

                                    if live_buy_price:
                                        # CRITICAL FIX: BOTH profitability and volatility checks must run independently
                                        # OLD BUG: Checks were in if/else structure - volatility never ran if profitability passed
                                        # NEW: Both checks run, either can abort the batch

                                        # CHECK 1: PROFITABILITY - Will this batch lose money?
                                        planned_sell_price = _find_planned_sell_price(action.good, route, segment_index)
                                        if planned_sell_price:
                                            # Apply degradation model to expected sell price
                                            # Account for the TOTAL units we plan to sell (already bought + remaining batches)
                                            total_planned_units = total_units_to_buy
                                            expected_sell_price = estimate_sell_price_with_degradation(planned_sell_price, total_planned_units)

                                            # Check if this batch would be unprofitable with degradation factored in
                                            if live_buy_price >= expected_sell_price:
                                                # CRITICAL FIX: Before aborting, query LIVE sell market price
                                                # The cached sell price from route may be stale (>1 hour old)
                                                # This prevents false positive circuit breaker triggers

                                                # Find the planned sell destination waypoint
                                                planned_sell_waypoint = _find_planned_sell_destination(action.good, route, segment_index)

                                                live_sell_price = None
                                                if planned_sell_waypoint:
                                                    logging.warning(f"  ⚠️  Circuit breaker triggered with cached sell price")
                                                    logging.warning(f"  Verifying with LIVE market data from {planned_sell_waypoint}...")

                                                    try:
                                                        # Query live API for sell market
                                                        sell_market = api.get_market(system, planned_sell_waypoint)
                                                        if sell_market:
                                                            for good_data in sell_market.get('tradeGoods', []):
                                                                if good_data['symbol'] == action.good:
                                                                    live_sell_price = good_data.get('purchasePrice')
                                                                    break
                                                    except Exception as e:
                                                        logging.warning(f"  ⚠️  Failed to query live sell market: {e}")

                                                # If we got live sell price, use it; otherwise use cached
                                                if live_sell_price:
                                                    # Re-check profitability with LIVE sell price
                                                    live_expected_sell_price = estimate_sell_price_with_degradation(live_sell_price, total_planned_units)

                                                    if live_buy_price >= live_expected_sell_price:
                                                        # Still unprofitable with live data - abort
                                                        logging.error("="*70)
                                                        logging.error(f"🚨 CIRCUIT BREAKER: UNPROFITABLE BATCH CONFIRMED WITH LIVE DATA!")
                                                        logging.error("="*70)
                                                        logging.error(f"  Live buy price: {live_buy_price:,} cr/unit")
                                                        logging.error(f"  Live sell price (verified): {live_sell_price:,} cr/unit")
                                                        logging.error(f"  Expected sell price (w/ degradation): {live_expected_sell_price:,} cr/unit")
                                                        if total_planned_units > 20:
                                                            degradation_pct = ((live_sell_price - live_expected_sell_price) / live_sell_price) * 100
                                                            logging.error(f"  Price degradation: -{degradation_pct:.1f}% ({total_planned_units} units total)")
                                                        logging.error(f"  Would lose: {live_buy_price - live_expected_sell_price:,} cr/unit")
                                                        logging.error(f"  🛡️  REMAINING BATCHES ABORTED - Partial purchase salvaged")
                                                        logging.error(f"  Purchased so far: {total_units_purchased} units for {total_batch_cost:,} credits")
                                                        logging.error("="*70)
                                                        batch_aborted = True
                                                        break
                                                    else:
                                                        # FALSE POSITIVE: Cached price was stale, trade is actually profitable!
                                                        profit_with_live = live_expected_sell_price - live_buy_price
                                                        logging.warning("="*70)
                                                        logging.warning(f"✅ CIRCUIT BREAKER FALSE POSITIVE PREVENTED!")
                                                        logging.warning("="*70)
                                                        logging.warning(f"  Cached sell price (stale): {planned_sell_price:,} cr/unit")
                                                        logging.warning(f"  Live sell price (fresh): {live_sell_price:,} cr/unit")
                                                        logging.warning(f"  Live buy price: {live_buy_price:,} cr/unit")
                                                        logging.warning(f"  Expected sell (w/ degradation): {live_expected_sell_price:,} cr/unit")
                                                        logging.warning(f"  ✅ Trade IS profitable: +{profit_with_live:,} cr/unit")
                                                        logging.warning(f"  Proceeding with batch {batch_num}...")
                                                        logging.warning("="*70)
                                                        # Continue with purchase
                                                else:
                                                    # Couldn't get live sell price - use cached price (conservative abort)
                                                    logging.error("="*70)
                                                    logging.error(f"🚨 CIRCUIT BREAKER: UNPROFITABLE BATCH DETECTED BEFORE BATCH {batch_num}!")
                                                    logging.error("="*70)
                                                    logging.error(f"  Live buy price: {live_buy_price:,} cr/unit")
                                                    logging.error(f"  Planned sell price (cached): {planned_sell_price:,} cr/unit")
                                                    logging.error(f"  Expected sell price (w/ degradation): {expected_sell_price:,} cr/unit")
                                                    logging.error(f"  ⚠️  Could not verify with live sell market data")
                                                    if total_planned_units > 20:
                                                        degradation_pct = ((planned_sell_price - expected_sell_price) / planned_sell_price) * 100
                                                        logging.error(f"  Price degradation: -{degradation_pct:.1f}% ({total_planned_units} units total)")
                                                    logging.error(f"  Would lose: {live_buy_price - expected_sell_price:,} cr/unit")
                                                    logging.error(f"  🛡️  REMAINING BATCHES ABORTED - Partial purchase salvaged")
                                                    logging.error(f"  Purchased so far: {total_units_purchased} units for {total_batch_cost:,} credits")
                                                    logging.error("="*70)
                                                    batch_aborted = True
                                                    break
                                        else:
                                            # No planned sell price - can't validate profitability
                                            # If extreme volatility, abort conservatively
                                            if batch_num == 1 and action.price_per_unit > 0:
                                                price_change_pct = ((live_buy_price - action.price_per_unit) / action.price_per_unit) * 100
                                                if price_change_pct > 50:
                                                    logging.error("="*70)
                                                    logging.error("🚨 CIRCUIT BREAKER: EXTREME VOLATILITY + NO SELL PRICE!")
                                                    logging.error("="*70)
                                                    logging.error(f"  Price spike: {price_change_pct:.1f}%")
                                                    logging.error(f"  No planned sell action found for {action.good}")
                                                    logging.error(f"  Cannot validate trade profitability")
                                                    logging.error(f"  🛡️  ALL BATCHES ABORTED - Too risky without sell price")
                                                    logging.error(f"  Purchased so far: {total_units_purchased} units for {total_batch_cost:,} credits")
                                                    logging.error("="*70)
                                                    batch_aborted = True
                                                    break

                                        # VOLATILITY MONITORING: Log price changes (informational only, not a circuit breaker)
                                        # Profitability check above is the ONLY circuit breaker
                                        if batch_num == 1 and action.price_per_unit > 0:
                                            # Log batch 1 price change vs planned
                                            price_change_pct = ((live_buy_price - action.price_per_unit) / action.price_per_unit) * 100

                                            if abs(price_change_pct) > 5:
                                                logging.warning(f"    ⚠️  Batch {batch_num} price: {action.price_per_unit:,} (planned) → {live_buy_price:,} ({price_change_pct:+.1f}%)")

                                            # Log high volatility warning (but don't abort if profitability check passed)
                                            if price_change_pct > 30:
                                                if planned_sell_price:
                                                    profit_margin = expected_sell_price - live_buy_price
                                                    profit_margin_pct = (profit_margin / expected_sell_price) * 100
                                                    logging.warning("="*70)
                                                    logging.warning(f"⚠️  HIGH VOLATILITY DETECTED BEFORE BATCH 1")
                                                    logging.warning("="*70)
                                                    logging.warning(f"  Planned price: {action.price_per_unit:,} cr/unit")
                                                    logging.warning(f"  Current price: {live_buy_price:,} cr/unit")
                                                    logging.warning(f"  Increase: {price_change_pct:.1f}%")
                                                    logging.warning(f"  ✅ Trade still profitable: +{profit_margin:,} cr/unit ({profit_margin_pct:.1f}% margin)")
                                                    logging.warning(f"  Proceeding with purchase...")
                                                    logging.warning("="*70)
                                        elif batch_num > 1 and initial_batch_price is not None:
                                            # Log batch 2+ price change vs batch 1
                                            price_change_pct = ((live_buy_price - initial_batch_price) / initial_batch_price) * 100 if initial_batch_price > 0 else 0

                                            if abs(price_change_pct) > 5:
                                                logging.warning(f"    ⚠️  Batch {batch_num} price: {initial_batch_price:,} → {live_buy_price:,} ({price_change_pct:+.1f}%)")

                                            # Log inter-batch volatility warning (but don't abort if profitability check passed above)
                                            if price_change_pct > 30:
                                                logging.warning("="*70)
                                                logging.warning(f"⚠️  INTER-BATCH PRICE SPIKE DETECTED BEFORE BATCH {batch_num}")
                                                logging.warning("="*70)
                                                logging.warning(f"  Baseline (batch 1): {initial_batch_price:,} cr/unit")
                                                logging.warning(f"  Current (batch {batch_num}): {live_buy_price:,} cr/unit")
                                                logging.warning(f"  Increase: {price_change_pct:.1f}%")
                                                if planned_sell_price:
                                                    profit_margin = expected_sell_price - live_buy_price
                                                    profit_margin_pct = (profit_margin / expected_sell_price) * 100
                                                    logging.warning(f"  ✅ Trade still profitable: +{profit_margin:,} cr/unit ({profit_margin_pct:.1f}% margin)")
                                                logging.warning(f"  Proceeding with batch {batch_num}...")
                                                logging.warning("="*70)
                                    else:
                                        logging.warning(f"    ⚠️  No live price data for batch {batch_num} - proceeding with caution")

                            except Exception as e:
                                logging.warning(f"    ⚠️  Market check failed for batch {batch_num}: {e}, proceeding with caution...")

                            # Execute batch purchase if not aborted
                            if not batch_aborted:
                                # CARGO OVERFLOW FIX: Check current cargo space before each batch
                                # Critical for batch purchasing where cargo accumulates across batches
                                current_ship_data = ship.get_status()
                                current_cargo_units = sum(item['units'] for item in current_ship_data['cargo']['inventory'])
                                cargo_available = current_ship_data['cargo']['capacity'] - current_cargo_units

                                if cargo_available <= 0:
                                    logging.error("="*70)
                                    logging.error(f"🚨 CARGO FULL BEFORE BATCH {batch_num}!")
                                    logging.error("="*70)
                                    logging.error(f"  Current cargo: {current_cargo_units}/{current_ship_data['cargo']['capacity']} units")
                                    logging.error(f"  Batch {batch_num}: Cannot purchase {units_this_batch} units")
                                    logging.error(f"  Total purchased so far: {total_units_purchased}/{total_units_to_buy} units")
                                    logging.error(f"  🛡️  REMAINING BATCHES ABORTED - Cargo full")
                                    logging.error("="*70)
                                    batch_aborted = True
                                    break  # Exit batch loop, continue to next action

                                if units_this_batch > cargo_available:
                                    logging.warning("="*70)
                                    logging.warning(f"⚠️  BATCH {batch_num} REDUCED: Insufficient cargo space")
                                    logging.warning("="*70)
                                    logging.warning(f"  Current cargo: {current_cargo_units}/{current_ship_data['cargo']['capacity']} units")
                                    logging.warning(f"  Available space: {cargo_available} units")
                                    logging.warning(f"  Planned batch: {units_this_batch} units")
                                    logging.warning(f"  Adjusted batch: {cargo_available} units")
                                    logging.warning("="*70)
                                    units_this_batch = cargo_available

                                batch_transaction = ship.buy(action.good, units_this_batch)
                                if not batch_transaction:
                                    logging.error(f"  ❌ Batch {batch_num} purchase failed for {action.good}")
                                    logging.error("  Route execution aborted")
                                    _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index)
                                    return False

                                batch_cost = batch_transaction['totalPrice']
                                batch_price_per_unit = batch_cost / batch_transaction['units'] if batch_transaction['units'] > 0 else 0

                                # Update database with ACTUAL transaction price from this batch
                                _update_market_price_from_transaction(
                                    db=db,
                                    waypoint=action.waypoint,
                                    good=action.good,
                                    transaction_type='PURCHASE',
                                    price_per_unit=int(batch_price_per_unit),
                                    logger=logging.getLogger(__name__)
                                )

                                # POST-BATCH VALIDATION: Check actual transaction price vs PLANNED SELL PRICE
                                # DEGRADATION FIX: Account for expected price erosion during batch selling
                                # Establish baseline from FIRST batch's ACTUAL transaction price
                                if initial_batch_price is None and batch_num == 1:
                                    initial_batch_price = batch_price_per_unit
                                    logging.info(f"    ✅ Baseline price established from actual batch 1 transaction: {initial_batch_price:,.0f} cr/unit")

                                # Check profitability: Compare batch price vs planned sell price with degradation
                                planned_sell_price = _find_planned_sell_price(action.good, route, segment_index)

                                if planned_sell_price:
                                    # Apply degradation model to expected sell price
                                    # Account for TOTAL units purchased so far + this batch
                                    total_units_now = total_units_purchased + batch_transaction['units']
                                    expected_sell_price = estimate_sell_price_with_degradation(planned_sell_price, total_units_now)

                                    # Calculate profitability for this batch with degradation factored in
                                    profit_per_unit = expected_sell_price - batch_price_per_unit
                                    is_batch_unprofitable = batch_price_per_unit >= expected_sell_price

                                    if is_batch_unprofitable:
                                        logging.error("="*70)
                                        logging.error(f"🚨 CIRCUIT BREAKER: UNPROFITABLE BATCH TRANSACTION (BATCH {batch_num})!")
                                        logging.error("="*70)
                                        logging.error(f"  Actual buy price: {batch_price_per_unit:,.0f} cr/unit")
                                        logging.error(f"  Planned sell price (cached): {planned_sell_price:,} cr/unit")
                                        logging.error(f"  Expected sell price (w/ degradation): {expected_sell_price:,} cr/unit")
                                        if total_units_now > 20:
                                            degradation_pct = ((planned_sell_price - expected_sell_price) / planned_sell_price) * 100
                                            logging.error(f"  Price degradation: -{degradation_pct:.1f}% ({total_units_now} units)")
                                        logging.error(f"  Loss per unit: {profit_per_unit:,.0f} cr")
                                        logging.error(f"  Already spent on batch {batch_num}: {batch_cost:,} credits")
                                        logging.error(f"  Total purchased: {total_units_now} units")
                                        logging.error(f"  Total spent: {total_batch_cost + batch_cost:,} credits")
                                        logging.error(f"  🛡️  REMAINING BATCHES ABORTED")
                                        logging.error("="*70)
                                        # Mark as aborted but still account for this batch
                                        batch_aborted = True
                                else:
                                    # Fallback: No sell action found, use volatility-based check
                                    if initial_batch_price is not None and batch_num > 1:
                                        actual_price_change_pct = ((batch_price_per_unit - initial_batch_price) / initial_batch_price) * 100

                                        if actual_price_change_pct > 30:
                                            logging.error("="*70)
                                            logging.error(f"🚨 CIRCUIT BREAKER: ACTUAL BATCH PRICE SPIKE (BATCH {batch_num})!")
                                            logging.error("="*70)
                                            logging.error(f"  Baseline (batch 1): {initial_batch_price:,} cr/unit")
                                            logging.error(f"  Actual (batch {batch_num}): {batch_price_per_unit:,.0f} cr/unit")
                                            logging.error(f"  Increase: {actual_price_change_pct:.1f}%")
                                            logging.error(f"  Already spent on batch {batch_num}: {batch_cost:,} credits")
                                            logging.error(f"  Total purchased: {total_units_purchased + batch_transaction['units']} units")
                                            logging.error(f"  Total spent: {total_batch_cost + batch_cost:,} credits")
                                            logging.error("="*70)
                                            # Mark as aborted but still account for this batch
                                            batch_aborted = True

                                # Update totals
                                total_batch_cost += batch_cost
                                total_units_purchased += batch_transaction['units']
                                units_remaining -= batch_transaction['units']

                                logging.info(f"    ✅ Batch {batch_num} complete: {batch_transaction['units']} units @ {batch_price_per_unit:,.0f} cr/unit = {batch_cost:,} credits")

                                # Stop if we detected a spike after this batch
                                if batch_aborted:
                                    break

                        # Final batch summary
                        if batch_aborted:
                            logging.warning(f"  ⚠️  Partial purchase: {total_units_purchased}/{total_units_to_buy} units ({total_batch_cost:,} credits)")
                            logging.warning(f"  Remaining {units_remaining} units NOT purchased due to price spike")

                            # Smart skip decision: Partial purchase, salvage and continue if possible
                            current_ship_data = ship.get_status()
                            current_cargo = {item['symbol']: item['units'] for item in current_ship_data['cargo']['inventory']}
                            current_agent = api.get_agent()
                            current_credits = current_agent['credits'] if current_agent else 0

                            should_skip, reason = should_skip_segment(
                                segment_index=segment_index,
                                failure_reason="BATCH BUY price spike",
                                dependencies=dependencies,
                                route=route,
                                current_cargo=current_cargo,
                                current_credits=current_credits
                            )

                            if should_skip:
                                logging.warning(f"Smart skip decision: {reason}")
                                remaining_segments = route.segments[segment_index + 1:]
                                skip_segment_and_cleanup_tiered(
                                    segment=segment,
                                    ship=ship,
                                    api=api,
                                    db=db,
                                    navigator=navigator,
                                    logger=logging.getLogger(__name__),
                                    remaining_segments=remaining_segments,
                                    ship_capacity=ship_data['cargo']['capacity']
                                )
                                skipped_segments.add(segment_index)
                                break  # Exit action loop, continue to next segment
                            else:
                                logging.error(f"Cannot skip: {reason}")
                                logging.error("  Aborting route to prevent further losses")
                                # CRITICAL FIX: Only salvage the unprofitable item (action.good)
                                # Keep other cargo for future profitable segments
                                _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index, unprofitable_item=action.good)
                                return False
                        else:
                            logging.info(f"  ✅ Batch purchase complete: {total_units_purchased} units for {total_batch_cost:,} credits")

                        segment_costs += total_batch_cost
                        total_costs += total_batch_cost

                elif action.action == 'SELL':
                    # Sell cargo with live monitoring
                    logging.info(f"  💵 Selling {action.units}x {action.good} @ {action.price_per_unit:,} = {action.total_value:,} credits")

                    # Get fresh market data for sell price monitoring
                    live_sell_price = None
                    sell_trade_volume = None
                    try:
                        live_market = api.get_market(system, action.waypoint)
                        if live_market:
                            for good in live_market.get('tradeGoods', []):
                                if good['symbol'] == action.good:
                                    live_sell_price = good.get('purchasePrice')  # What market pays us
                                    sell_trade_volume = good.get('tradeVolume')
                                    break

                            if live_sell_price:
                                price_change_pct = ((live_sell_price - action.price_per_unit) / action.price_per_unit) * 100 if action.price_per_unit > 0 else 0

                                if abs(price_change_pct) > 5:
                                    logging.warning(f"  ⚠️  Sell price changed: {action.price_per_unit:,} → {live_sell_price:,} ({price_change_pct:+.1f}%)")

                                # CIRCUIT BREAKER: Abort if sell price crashed
                                if price_change_pct < -30:
                                    logging.error("="*70)
                                    logging.error("🚨 CIRCUIT BREAKER: SELL PRICE CRASH!")
                                    logging.error("="*70)
                                    logging.error(f"  Expected: {action.price_per_unit:,} cr/unit")
                                    logging.error(f"  Current: {live_sell_price:,} cr/unit")
                                    logging.error(f"  Drop: {price_change_pct:.1f}%")
                                    logging.error("="*70)

                                    # Smart skip decision: Sell price crashed, salvage and continue if possible
                                    current_ship_data = ship.get_status()
                                    current_cargo = {item['symbol']: item['units'] for item in current_ship_data['cargo']['inventory']}
                                    current_agent = api.get_agent()
                                    current_credits = current_agent['credits'] if current_agent else 0

                                    should_skip, reason = should_skip_segment(
                                        segment_index=segment_index,
                                        failure_reason="SELL price crash",
                                        dependencies=dependencies,
                                        route=route,
                                        current_cargo=current_cargo,
                                        current_credits=current_credits
                                    )

                                    if should_skip:
                                        logging.warning(f"Smart skip decision: {reason}")
                                        remaining_segments = route.segments[segment_index + 1:]
                                        skip_segment_and_cleanup_tiered(
                                            segment=segment,
                                            ship=ship,
                                            api=api,
                                            db=db,
                                            navigator=navigator,
                                            logger=logging.getLogger(__name__),
                                            remaining_segments=remaining_segments,
                                            ship_capacity=ship_data['cargo']['capacity']
                                        )
                                        skipped_segments.add(segment_index)
                                        break  # Exit action loop, continue to next segment
                                    else:
                                        logging.error(f"Cannot skip: {reason}")
                                        logging.error("  Route execution aborted to prevent loss")
                                        _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index)
                                        return False
                    except Exception as e:
                        logging.warning(f"  ⚠️  Live market check failed: {e}, proceeding with planned sale...")

                    # Execute sale with live monitoring
                    transaction = ship.sell(
                        action.good,
                        action.units,
                        max_per_transaction=sell_trade_volume,
                        check_market_prices=True,
                        min_acceptable_price=live_sell_price if live_sell_price else action.price_per_unit
                    )

                    if not transaction:
                        logging.error(f"  ❌ Sale failed for {action.good}")
                        logging.error("  Route execution aborted")
                        _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index)
                        return False

                    # Update database with ACTUAL transaction price
                    actual_revenue = transaction['totalPrice']
                    actual_units_sold = transaction['units']
                    actual_price_per_unit = actual_revenue / actual_units_sold if actual_units_sold > 0 else 0
                    _update_market_price_from_transaction(
                        db=db,
                        waypoint=action.waypoint,
                        good=action.good,
                        transaction_type='SELL',
                        price_per_unit=int(actual_price_per_unit),
                        logger=logging.getLogger(__name__)
                    )

                    # Check if sale was aborted mid-batch due to price collapse
                    if transaction.get('aborted'):
                        remaining = transaction.get('remaining_units', 0)
                        logging.error("="*70)
                        logging.error("🚨 CIRCUIT BREAKER: SALE ABORTED MID-BATCH!")
                        logging.error("="*70)
                        logging.error(f"  Sold: {transaction['units']} units")
                        logging.error(f"  Remaining: {remaining} units (unsold due to price collapse)")
                        logging.error("="*70)

                        # Smart skip decision: Sale aborted, salvage remaining and continue if possible
                        current_ship_data = ship.get_status()
                        current_cargo = {item['symbol']: item['units'] for item in current_ship_data['cargo']['inventory']}
                        current_agent = api.get_agent()
                        current_credits = current_agent['credits'] if current_agent else 0

                        should_skip, reason = should_skip_segment(
                            segment_index=segment_index,
                            failure_reason="SALE aborted mid-batch",
                            dependencies=dependencies,
                            route=route,
                            current_cargo=current_cargo,
                            current_credits=current_credits
                        )

                        if should_skip:
                            logging.warning(f"Smart skip decision: {reason}")
                            remaining_segments = route.segments[segment_index + 1:]
                            skip_segment_and_cleanup_tiered(
                                segment=segment,
                                ship=ship,
                                api=api,
                                db=db,
                                navigator=navigator,
                                logger=logging.getLogger(__name__),
                                remaining_segments=remaining_segments,
                                ship_capacity=ship_data['cargo']['capacity']
                            )
                            skipped_segments.add(segment_index)
                            break  # Exit action loop, continue to next segment
                        else:
                            logging.error(f"Cannot skip: {reason}")
                            logging.error("  Route execution aborted")
                            # CRITICAL FIX: Only salvage the unprofitable item (action.good)
                            # Keep other cargo for future profitable segments
                            _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index, unprofitable_item=action.good)
                            return False

                    actual_revenue = transaction['totalPrice']
                    segment_revenue += actual_revenue
                    total_revenue += actual_revenue

                    logging.info(f"  ✅ Sold {transaction['units']}x {action.good} for {actual_revenue:,} credits")

                # Rate limiting between actions
                time.sleep(0.6)

            # Segment complete - calculate segment profit
            segment_profit = segment_revenue - segment_costs

            logging.info(f"\n📊 Segment {segment_num} complete:")
            logging.info(f"  Revenue: {segment_revenue:,} credits")
            logging.info(f"  Costs: {segment_costs:,} credits")
            logging.info(f"  Profit: {segment_profit:,} credits")

            # CIRCUIT BREAKER: Check if segment was unprofitable
            # CRITICAL FIX: Only check profitability on segments with SELL actions
            # BUY-only segments will naturally show negative profit (spent money, no revenue yet)
            # This is EXPECTED behavior mid-route, not a failure!
            has_buy_actions = any(action.action == 'BUY' for action in segment.actions_at_destination)
            has_sell_actions = any(action.action == 'SELL' for action in segment.actions_at_destination)

            # Only trigger circuit breaker if:
            # 1. Segment has SELL actions (completed a buy→sell cycle or sell-only) AND unprofitable
            # 2. OR segment has both BUY and SELL AND still unprofitable (bad pricing)
            should_check_profitability = has_sell_actions

            if segment_profit < 0 and not should_check_profitability:
                # BUY-only segment with negative profit - this is EXPECTED (mid-route)
                logging.info(f"  ℹ️  BUY-only segment shows negative profit (expected mid-route behavior)")
                logging.info(f"  Will evaluate profitability after completing buy→sell cycle")

            if segment_profit < 0 and should_check_profitability:
                logging.error("="*70)
                logging.error("🚨 CIRCUIT BREAKER: SEGMENT UNPROFITABLE!")
                logging.error("="*70)
                logging.error(f"  Segment {segment_num} lost {abs(segment_profit):,} credits")
                logging.error("="*70)

                # Smart skip decision: Segment was unprofitable, salvage and continue if possible
                current_ship_data = ship.get_status()
                current_cargo = {item['symbol']: item['units'] for item in current_ship_data['cargo']['inventory']}
                current_agent = api.get_agent()
                current_credits = current_agent['credits'] if current_agent else 0

                should_skip, reason = should_skip_segment(
                    segment_index=segment_index,
                    failure_reason="SEGMENT unprofitable",
                    dependencies=dependencies,
                    route=route,
                    current_cargo=current_cargo,
                    current_credits=current_credits
                )

                if should_skip:
                    logging.warning(f"Smart skip decision: {reason}")
                    remaining_segments = route.segments[segment_index + 1:]
                    skip_segment_and_cleanup_tiered(
                        segment=segment,
                        ship=ship,
                        api=api,
                        db=db,
                        navigator=navigator,
                        logger=logging.getLogger(__name__),
                        remaining_segments=remaining_segments,
                        ship_capacity=ship_data['cargo']['capacity']
                    )
                    skipped_segments.add(segment_index)
                    continue  # Continue to next segment (not break like action loop)
                else:
                    logging.error(f"Cannot skip: {reason}")
                    logging.error("  Aborting route to prevent further losses")
                    _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index)
                    return False

            # Get current credits to verify progress
            current_agent = api.get_agent()
            if current_agent:
                current_credits = current_agent['credits']
                net_change = current_credits - starting_credits
                logging.info(f"  Current credits: {current_credits:,} (net: {net_change:+,})")

        except Exception as e:
            logging.error("="*70)
            logging.error(f"🚨 CRITICAL ERROR during segment {segment_num}")
            logging.error("="*70)
            logging.error(f"Error: {str(e)}")
            logging.error("Route execution aborted")
            logging.error("="*70)
            import traceback
            logging.error(traceback.format_exc())
            _cleanup_stranded_cargo(ship, api, db, logging.getLogger(__name__), route, segment_index)
            return False

    # Route execution complete (may have skipped some segments)
    logging.info("\n" + "="*70)
    if len(skipped_segments) == 0:
        logging.info("✅ ALL SEGMENTS COMPLETE")
    else:
        executed_count = len(route.segments) - len(skipped_segments)
        logging.info(f"✅ ROUTE COMPLETE ({executed_count}/{len(route.segments)} segments executed)")
        logging.info(f"Skipped segments: {sorted(skipped_segments)}")
    logging.info("="*70)

    # Final accounting
    final_agent = api.get_agent()
    if final_agent:
        final_credits = final_agent['credits']
        actual_profit = final_credits - starting_credits

        logging.info(f"Starting credits: {starting_credits:,}")
        logging.info(f"Final credits: {final_credits:,}")
        logging.info(f"Actual profit: {actual_profit:,}")
        logging.info(f"Estimated profit: {route.total_profit:,}")

        if route.total_profit > 0:
            accuracy = (actual_profit / route.total_profit) * 100
            logging.info(f"Estimate accuracy: {accuracy:.1f}%")

        # Final circuit breaker: Check if overall route was profitable
        if actual_profit < 0:
            logging.warning("="*70)
            logging.warning("⚠️  WARNING: Overall route was unprofitable!")
            logging.warning("="*70)
            logging.warning(f"  Net loss: {abs(actual_profit):,} credits")
            logging.warning("  Market conditions changed unfavorably")
            logging.warning("="*70)
            # Don't return False here - we completed the route, just didn't make profit

    logging.info("="*70)
    return True


def trade_plan_operation(args):
    """Analyze and propose a multi-leg trading route without executing it."""
    from .common import get_api_client, get_database

    player_id = getattr(args, "player_id", None)
    ship_symbol = getattr(args, "ship", None)

    if not player_id:
        print("❌ --player-id required")
        return 1

    if not ship_symbol:
        print("❌ --ship required")
        return 1

    max_stops = getattr(args, "max_stops", 4) or 4
    try:
        max_stops = int(max_stops)
    except (TypeError, ValueError):
        print("❌ --max-stops must be an integer")
        return 1

    if max_stops < 2:
        print("❌ --max-stops must be at least 2")
        return 1

    try:
        api = get_api_client(player_id)
    except Exception as exc:  # Surface errors directly
        print(f"❌ {exc}")
        return 1

    db = get_database()
    ship = ShipController(api, ship_symbol)

    ship_data = ship.get_status()
    if not ship_data:
        print("❌ Failed to get ship status")
        return 1

    system_override = getattr(args, "system", None)
    system = system_override or ship_data['nav']['systemSymbol']
    start_waypoint = ship_data['nav']['waypointSymbol']

    cargo_capacity = ship_data['cargo']['capacity']
    ship_speed = ship_data['engine']['speed']
    fuel_capacity = ship_data['fuel']['capacity']
    current_fuel = ship_data['fuel']['current']

    # CRITICAL FIX: Extract actual ship cargo (may have residual from previous operations)
    starting_cargo = {item['symbol']: item['units']
                     for item in ship_data['cargo']['inventory']}

    agent = api.get_agent()
    if not agent:
        print("❌ Failed to get agent data")
        return 1

    starting_credits = agent['credits']

    optimizer = MultiLegTradeOptimizer(api, db, player_id)
    route = optimizer.find_optimal_route(
        start_waypoint=start_waypoint,
        system=system,
        max_stops=max_stops,
        cargo_capacity=cargo_capacity,
        starting_credits=starting_credits,
        ship_speed=ship_speed,
        fuel_capacity=fuel_capacity,
        current_fuel=current_fuel,
        starting_cargo=starting_cargo,
    )

    if not route:
        print("❌ No profitable route found")
        return 1

    summary = {
        "ship": ship_symbol,
        "player_id": player_id,
        "system": system,
        "start_waypoint": start_waypoint,
        "max_stops": max_stops,
        "total_profit": route.total_profit,
        "total_distance": route.total_distance,
        "total_fuel_cost": route.total_fuel_cost,
        "estimated_time_minutes": route.estimated_time_minutes,
        "segment_count": len(route.segments),
        "segments": [],
    }

    for index, segment in enumerate(route.segments, start=1):
        actions = [
            {
                "type": action.action,
                "good": action.good,
                "units": action.units,
                "price_per_unit": action.price_per_unit,
                "total_value": action.total_value,
            }
            for action in segment.actions_at_destination
        ]

        summary["segments"].append(
            {
                "index": index,
                "from_waypoint": segment.from_waypoint,
                "to_waypoint": segment.to_waypoint,
                "distance": segment.distance,
                "fuel_cost": segment.fuel_cost,
                "actions": actions,
                "cargo_after": segment.cargo_after,
                "credits_after": segment.credits_after,
                "cumulative_profit": segment.cumulative_profit,
            }
        )

    print(json.dumps(summary, indent=2))
    return 0


def create_fixed_route(
    api, db, player_id,
    current_waypoint,
    buy_waypoint,
    sell_waypoint,
    good,
    cargo_capacity,
    starting_credits,
    ship_speed,
    fuel_capacity,
    current_fuel
) -> Optional[MultiLegRoute]:
    """
    Create a fixed 2-stop route (buy → sell) without optimization

    This is the prescriptive mode for single-leg trading
    """
    from spacetraders_bot.core.utils import calculate_distance

    logging.info("="*70)
    logging.info("CREATING FIXED ROUTE")
    logging.info("="*70)
    logging.info(f"Route: {current_waypoint} → {buy_waypoint} → {sell_waypoint}")
    logging.info(f"Good: {good}")
    logging.info(f"Cargo capacity: {cargo_capacity}")

    # Get market data from database
    with db.transaction() as conn:
        buy_market_rows = db.get_market_data(conn, buy_waypoint, good)
        sell_market_rows = db.get_market_data(conn, sell_waypoint, good)

    # Extract first row (get_market_data returns List[Dict])
    buy_market = buy_market_rows[0] if buy_market_rows else None
    sell_market = sell_market_rows[0] if sell_market_rows else None

    if not buy_market or not sell_market:
        logging.error("Missing market data for route")
        return None

    buy_price = buy_market['sell_price']  # What we pay
    sell_price = sell_market['purchase_price']  # What we receive
    trade_volume = buy_market.get('trade_volume', cargo_capacity)

    logging.info(f"Buy price @ {buy_waypoint}: {buy_price:,} cr/unit")
    logging.info(f"Sell price @ {sell_waypoint}: {sell_price:,} cr/unit")
    logging.info(f"Spread: {sell_price - buy_price:,} cr/unit")

    # Look up waypoint coordinates from database
    # (calculate_distance expects coordinate dictionaries, not waypoint symbols)
    with db.connection() as conn:
        cursor = conn.cursor()

        # Get coordinates for current waypoint
        cursor.execute("SELECT x, y FROM waypoints WHERE waypoint_symbol = ?", (current_waypoint,))
        current_coords_row = cursor.fetchone()

        # Get coordinates for buy waypoint
        cursor.execute("SELECT x, y FROM waypoints WHERE waypoint_symbol = ?", (buy_waypoint,))
        buy_coords_row = cursor.fetchone()

        # Get coordinates for sell waypoint
        cursor.execute("SELECT x, y FROM waypoints WHERE waypoint_symbol = ?", (sell_waypoint,))
        sell_coords_row = cursor.fetchone()

    # Validate that we have all coordinates
    if not current_coords_row or not buy_coords_row or not sell_coords_row:
        missing = []
        if not current_coords_row:
            missing.append(current_waypoint)
        if not buy_coords_row:
            missing.append(buy_waypoint)
        if not sell_coords_row:
            missing.append(sell_waypoint)
        logging.error(f"Missing waypoint coordinate data for: {', '.join(missing)}")
        return None

    # Convert database rows to coordinate dictionaries
    current_coords = {'x': current_coords_row[0], 'y': current_coords_row[1]}
    buy_coords = {'x': buy_coords_row[0], 'y': buy_coords_row[1]}
    sell_coords = {'x': sell_coords_row[0], 'y': sell_coords_row[1]}

    # Calculate distances using coordinate dictionaries
    dist_to_buy = calculate_distance(current_coords, buy_coords)
    dist_buy_to_sell = calculate_distance(buy_coords, sell_coords)
    total_distance = dist_to_buy + dist_buy_to_sell

    # Estimate fuel costs
    fuel_to_buy = int(dist_to_buy * 1.1)  # CRUISE mode estimate
    fuel_buy_to_sell = int(dist_buy_to_sell * 1.1)
    total_fuel = fuel_to_buy + fuel_buy_to_sell

    # Calculate units to buy (limited by credits and cargo)
    max_units_by_credits = int((starting_credits * 0.85) / buy_price) if buy_price > 0 else cargo_capacity
    units_to_buy = min(cargo_capacity, max_units_by_credits, trade_volume if trade_volume else cargo_capacity)

    if units_to_buy <= 0:
        logging.error("Cannot afford any units")
        return None

    purchase_cost = units_to_buy * buy_price
    sale_revenue = units_to_buy * sell_price
    estimated_fuel_cost = total_fuel * 100  # Rough estimate: 100 cr/fuel
    profit = sale_revenue - purchase_cost - estimated_fuel_cost

    logging.info(f"Units to trade: {units_to_buy}")
    logging.info(f"Purchase cost: {purchase_cost:,}")
    logging.info(f"Sale revenue: {sale_revenue:,}")
    logging.info(f"Estimated profit: {profit:,}")

    if profit <= 0:
        logging.warning("Route not profitable based on current market data")
        return None

    # Build route segments
    segments = []

    # Segment 1: Current → Buy market
    if current_waypoint != buy_waypoint:
        segments.append(RouteSegment(
            from_waypoint=current_waypoint,
            to_waypoint=buy_waypoint,
            distance=dist_to_buy,
            fuel_cost=fuel_to_buy,
            actions_at_destination=[
                TradeAction(
                    waypoint=buy_waypoint,
                    good=good,
                    action='BUY',
                    units=units_to_buy,
                    price_per_unit=buy_price,
                    total_value=purchase_cost
                )
            ],
            cargo_after={good: units_to_buy},
            credits_after=starting_credits - purchase_cost,
            cumulative_profit=0
        ))

    # Segment 2: Buy market → Sell market
    segments.append(RouteSegment(
        from_waypoint=buy_waypoint,
        to_waypoint=sell_waypoint,
        distance=dist_buy_to_sell,
        fuel_cost=fuel_buy_to_sell,
        actions_at_destination=[
            TradeAction(
                waypoint=sell_waypoint,
                good=good,
                action='SELL',
                units=units_to_buy,
                price_per_unit=sell_price,
                total_value=sale_revenue
            )
        ],
        cargo_after={},
        credits_after=starting_credits - purchase_cost + sale_revenue,
        cumulative_profit=profit
    ))

    route = MultiLegRoute(
        segments=segments,
        total_profit=profit,
        total_distance=total_distance,
        total_fuel_cost=total_fuel,
        estimated_time_minutes=int((total_distance / ship_speed) * 4)  # Rough estimate
    )

    logging.info("="*70)
    logging.info("FIXED ROUTE CREATED")
    logging.info("="*70)
    logging.info(f"Segments: {len(segments)}")
    logging.info(f"Total distance: {total_distance} units")
    logging.info(f"Estimated profit: {profit:,} credits")
    logging.info("="*70)

    return route


def multileg_trade_operation(args):
    """Execute a multi-leg trading operation"""
    from .common import setup_logging, get_api_client, get_database
    from spacetraders_bot.core.ship import ShipController
    from datetime import datetime, timedelta

    log_file = setup_logging("multileg_trade", args.ship, getattr(args, 'log_level', 'INFO'), player_id=args.player_id)

    logging.info("="*70)
    logging.info("MULTI-LEG TRADING OPERATION")
    logging.info("="*70)

    api = get_api_client(args.player_id)
    db = get_database()
    ship = ShipController(api, args.ship)

    # Get ship status
    ship_data = ship.get_status()
    if not ship_data:
        logging.error("Failed to get ship status")
        return 1

    system = getattr(args, 'system', None) or ship_data['nav']['systemSymbol']
    current_waypoint = ship_data['nav']['waypointSymbol']
    cargo_capacity = getattr(args, 'cargo', None) or ship_data['cargo']['capacity']
    ship_speed = ship_data['engine']['speed']
    fuel_capacity = ship_data['fuel']['capacity']
    current_fuel = ship_data['fuel']['current']

    # Get current credits
    agent = api.get_agent()
    if not agent:
        logging.error("Failed to get agent data")
        return 1

    starting_credits = agent['credits']

    # Determine operation mode
    fixed_route_mode = bool(getattr(args, 'good', None) and
                            getattr(args, 'buy_from', None) and
                            getattr(args, 'sell_to', None))
    looping_mode = getattr(args, 'cycles', None) is not None or getattr(args, 'duration', None) is not None

    if fixed_route_mode:
        logging.info("Mode: FIXED-ROUTE (prescriptive trading)")
        logging.info(f"Route: {args.good} from {args.buy_from} → {args.sell_to}")
    else:
        logging.info("Mode: AUTONOMOUS (route optimization)")

    if looping_mode:
        cycles = getattr(args, 'cycles', None)
        duration = getattr(args, 'duration', None)
        if cycles is not None:
            if cycles == -1:
                logging.info("Looping: INFINITE")
            else:
                logging.info(f"Looping: {cycles} cycles")
        else:
            logging.info(f"Duration: {duration} hours")
    else:
        logging.info("Mode: ONE-SHOT")

    logging.info(f"Min profit threshold: {args.min_profit:,} credits")
    logging.info("="*70)

    # Initialize loop control
    if looping_mode:
        if getattr(args, 'duration', None):
            start_time = datetime.now()
            end_time = start_time + timedelta(hours=args.duration)
            cycles_remaining = float('inf')
        else:
            end_time = None
            cycles_remaining = args.cycles if args.cycles != -1 else float('inf')
    else:
        end_time = None
        cycles_remaining = 1

    cycle_num = 0
    total_profit = 0
    low_profit_breaker = CircuitBreaker(limit=3)

    # Main trading loop
    while cycles_remaining > 0:
        cycle_num += 1

        # Check time limit
        if end_time and datetime.now() >= end_time:
            logging.info("Duration limit reached")
            break

        if looping_mode:
            logging.info(f"\n{'='*70}")
            logging.info(f"CYCLE {cycle_num}")
            if cycles_remaining != float('inf'):
                logging.info(f"Remaining: {int(cycles_remaining)} cycles")
            logging.info('='*70)

        # Get current ship status
        ship_data = ship.get_status()
        if not ship_data:
            logging.error("Failed to get ship status")
            return 1

        current_waypoint = ship_data['nav']['waypointSymbol']
        current_fuel = ship_data['fuel']['current']

        # Get current credits
        agent = api.get_agent()
        if not agent:
            logging.error("Failed to get agent data")
            return 1

        cycle_start_credits = agent['credits']

        # Find or create route
        if fixed_route_mode:
            route = create_fixed_route(
                api, db, args.player_id,
                current_waypoint,
                args.buy_from,
                args.sell_to,
                args.good,
                cargo_capacity,
                cycle_start_credits,
                ship_speed,
                fuel_capacity,
                current_fuel
            )
        else:
            optimizer = MultiLegTradeOptimizer(api, db, args.player_id)
            route = optimizer.find_optimal_route(
                start_waypoint=current_waypoint,
                system=system,
                max_stops=args.max_stops,
                cargo_capacity=cargo_capacity,
                starting_credits=cycle_start_credits,
                ship_speed=ship_speed,
                fuel_capacity=fuel_capacity,
                current_fuel=current_fuel
            )

        if not route:
            logging.error("No profitable route found")
            if looping_mode:
                logging.warning("Breaking loop - no profitable routes available")
            return 1

        # Execute the route
        logging.info("\n" + "="*70)
        logging.info("EXECUTING MULTI-LEG ROUTE")
        logging.info("="*70)

        success = execute_multileg_route(route, ship, api, db, args.player_id)

        if not success:
            logging.error("\n" + "="*70)
            logging.error("❌ MULTI-LEG ROUTE FAILED")
            logging.error("="*70)
            return 1

        # Calculate cycle profit
        final_agent = api.get_agent()
        cycle_end_credits = final_agent['credits'] if final_agent else cycle_start_credits
        cycle_profit = cycle_end_credits - cycle_start_credits
        total_profit += cycle_profit

        logging.info("\n" + "="*70)
        if looping_mode:
            logging.info(f"CYCLE {cycle_num} COMPLETE")
        else:
            logging.info("✅ MULTI-LEG ROUTE COMPLETE")
        logging.info("="*70)
        logging.info(f"Cycle profit: {cycle_profit:,}")
        logging.info(f"Estimated profit: {route.total_profit:,}")
        logging.info(f"Accuracy: {(cycle_profit/route.total_profit*100) if route.total_profit > 0 else 0:.1f}%")

        if looping_mode:
            logging.info(f"Total profit: {total_profit:,}")
            logging.info(f"Average profit/cycle: {total_profit // cycle_num:,}")

        logging.info("="*70)

        # Circuit breaker: Check profitability for looping mode
        if looping_mode:
            if cycle_profit < args.min_profit:
                failures = low_profit_breaker.record_failure()
                logging.warning(
                    "Low profit (%s < %s)",
                    f"{cycle_profit:,}",
                    f"{args.min_profit:,}",
                )
                logging.warning("Consecutive low cycles: %s", failures)

                if low_profit_breaker.tripped():
                    logging.error(
                        "🚨 %s consecutive low-profit cycles", failures
                    )
                    logging.error("🛑 STOPPING")
                    break
            else:
                low_profit_breaker.record_success()

            if cycle_profit < 0:
                logging.error(f"🚨 CIRCUIT BREAKER: NEGATIVE PROFIT ({cycle_profit:,})")
                logging.error("🛑 STOPPING")
                break

        # Decrement cycles
        if cycles_remaining != float('inf'):
            cycles_remaining -= 1

        # Brief pause between cycles
        if looping_mode and cycles_remaining > 0:
            time.sleep(2)

    # Final summary
    final_agent = api.get_agent()
    final_credits = final_agent['credits'] if final_agent else starting_credits

    logging.info(f"\n{'='*70}")
    logging.info("OPERATION COMPLETE")
    logging.info('='*70)
    logging.info(f"Starting credits: {starting_credits:,}")
    logging.info(f"Final credits: {final_credits:,}")
    logging.info(f"Total profit: {total_profit:,}")
    logging.info(f"Cycles completed: {cycle_num}")
    if cycle_num > 0:
        logging.info(f"Average profit/cycle: {total_profit // cycle_num:,}")
    logging.info('='*70)

    return 0


def fleet_trade_optimize_operation(args):
    """
    Fleet trade route optimization operation - finds conflict-free profitable routes for multiple ships.

    Args:
        args: CLI arguments with player_id, ships (comma-separated), system, max_stops

    Returns:
        0 on success, 1 on failure
    """
    from .common import setup_logging, get_api_client
    from ..core.database import Database
    from ..core.ship_controller import ShipController

    log_file = setup_logging("fleet_trade_optimize", "fleet", getattr(args, 'log_level', 'INFO'), player_id=args.player_id)

    print("=" * 70)
    print("FLEET TRADE ROUTE OPTIMIZATION")
    print("=" * 70)
    print(f"System: {args.system}")
    print(f"Max Stops: {args.max_stops}")
    print("=" * 70)

    # Parse ship list
    ship_symbols = [s.strip() for s in args.ships.split(',')]
    print(f"\nOptimizing routes for {len(ship_symbols)} ships:")
    for ship_symbol in ship_symbols:
        print(f"  - {ship_symbol}")
    print()

    # Initialize components
    api = get_api_client(args.player_id)
    db = Database()

    # Get agent starting credits
    agent = api.get_agent()
    if not agent:
        print("❌ Failed to get agent data")
        return 1
    starting_credits = agent['credits']

    # Get ship data for all ships
    ships = []
    for ship_symbol in ship_symbols:
        ship_data = api.get(f"/my/ships/{ship_symbol}")
        if not ship_data or 'data' not in ship_data:
            print(f"❌ Failed to get data for ship {ship_symbol}")
            return 1
        ships.append(ship_data['data'])

    # Initialize optimizer
    print("Initializing fleet optimizer...")
    optimizer = FleetTradeOptimizer(api, db, player_id=args.player_id)

    # Run optimization
    print(f"\nOptimizing fleet routes in {args.system}...\n")
    try:
        result = optimizer.optimize_fleet(
            ships=ships,
            system=args.system,
            max_stops=args.max_stops,
            starting_credits=starting_credits,
        )
    except Exception as e:
        print(f"\n❌ Optimization failed: {e}")
        import traceback
        traceback.print_exc()
        return 1

    if not result or not result.get('ship_routes'):
        print("\n❌ No routes found for fleet")
        return 1

    # Display results
    print("\n" + "=" * 70)
    print("FLEET OPTIMIZATION RESULTS")
    print("=" * 70)
    print(f"Total Fleet Profit: {result['total_fleet_profit']:,} credits\n")

    # Create ship lookup
    ship_lookup = {s['symbol']: s for s in ships}

    for i, (ship_symbol, route) in enumerate(result['ship_routes'].items(), 1):
        print(f"\n{'='*70}")
        print(f"SHIP {i}/{len(result['ship_routes'])}: {ship_symbol}")
        print('='*70)

        if not route or not route.segments:
            print("  No profitable route found")
            continue

        # Get ship's current location
        ship = ship_lookup[ship_symbol]
        start_waypoint = ship['nav']['waypointSymbol']

        # Show route summary
        waypoints = [start_waypoint] + [seg.to_waypoint for seg in route.segments]
        route_str = " → ".join(waypoints)
        print(f"Route: {route_str}")
        print(f"Estimated Profit: {route.total_profit:,} credits")
        print(f"Segments: {len(route.segments)}")
        print(f"Total Distance: {route.total_distance:.0f} units")
        print(f"Est. Duration: {route.estimated_time_minutes:.0f} minutes")

        # Show BUY actions (the ones that matter for conflicts)
        print(f"\nBUY Actions (reserved for conflict avoidance):")
        for j, segment in enumerate(route.segments, 1):
            for action in segment.actions_at_destination:
                if action.action == 'BUY':
                    print(f"  {j}. {segment.to_waypoint}: BUY {action.units}x {action.good}")

    # Show conflict status
    print(f"\n{'='*70}")
    print("CONFLICT ANALYSIS")
    print('='*70)
    reserved = result.get('reserved_pairs', set())
    print(f"Total Reserved (Resource, Waypoint) Pairs: {len(reserved)}")
    print(f"Conflicts Detected: {result.get('conflicts', 0)}")

    if result.get('conflicts', 0) == 0:
        print("✅ All routes are conflict-free!")
    else:
        print("⚠️  WARNING: Conflicts detected between routes")

    print("\n" + "=" * 70)
    print("\nUse these parameters to start daemons:")
    print("-" * 70)

    for ship_symbol, route in result['ship_routes'].items():
        if not route or not route.segments:
            continue

        # Get ship's current location
        ship = ship_lookup[ship_symbol]
        start_waypoint = ship['nav']['waypointSymbol']

        # Build daemon command
        waypoints = [start_waypoint] + [seg.to_waypoint for seg in route.segments]

        # For simplicity, recommend multileg-trade with system parameter
        print(f"\n# {ship_symbol}")
        print(f"spacetraders-bot daemon start trade \\")
        print(f"  --player-id {args.player_id} \\")
        print(f"  --ship {ship_symbol} \\")
        print(f"  --system {args.system} \\")
        print(f"  --max-stops {args.max_stops} \\")
        print(f"  --cycles 10")

    print("\n" + "=" * 70)

    return 0
