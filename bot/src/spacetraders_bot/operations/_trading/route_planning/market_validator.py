"""
Market Validator

Single Responsibility: Validate market data quality and freshness.

Ensures that market data used for trading decisions is reliable and recent enough
for accurate profit calculations.
"""

import logging
from datetime import datetime, timezone
from typing import Dict, Optional, Tuple


class MarketValidator:
    """
    Market data validation service

    Responsibilities:
    - Check data freshness (age < 1 hour)
    - Detect aging data (0.5 - 1 hour)
    - Detect stale data (> 1 hour)
    - Parse and validate timestamps
    """

    # Freshness thresholds (in hours)
    FRESH_THRESHOLD_HOURS = 0.5
    AGING_THRESHOLD_HOURS = 1.0

    def __init__(self, logger: Optional[logging.Logger] = None):
        """
        Initialize market validator

        Args:
            logger: Optional logger (creates default if not provided)
        """
        self.logger = logger or logging.getLogger(__name__)

    def is_market_data_fresh(
        self,
        record: Dict,
        waypoint: str,
        good: str,
        action_type: str
    ) -> bool:
        """
        Check if market data is fresh enough for trading

        Args:
            record: Market data record with 'last_updated' timestamp
            waypoint: Waypoint symbol
            good: Good symbol
            action_type: 'buy' or 'sell' (for logging context)

        Returns:
            True if data is fresh (<1 hour), False otherwise
        """
        last_updated = record.get('last_updated')
        if not last_updated:
            return True  # No timestamp, assume fresh

        age_hours = self.get_data_age_hours(last_updated)
        if age_hours is None:
            return True  # Timestamp parsing failed, assume fresh

        if age_hours > self.AGING_THRESHOLD_HOURS:
            self.logger.warning(
                f"⚠️  Skipping stale {action_type} data: {waypoint} {good} ({age_hours:.1f}h old)"
            )
            return False
        elif age_hours > self.FRESH_THRESHOLD_HOURS:
            self.logger.info(
                f"  ⏰ Aging {action_type} data: {waypoint} {good} ({age_hours:.1f}h old)"
            )

        return True

    def get_data_age_hours(self, timestamp_str: str) -> Optional[float]:
        """
        Parse timestamp and calculate age in hours

        Args:
            timestamp_str: ISO 8601 timestamp string (e.g., '2025-10-21T12:00:00.000Z')

        Returns:
            Age in hours, or None if timestamp parsing fails
        """
        try:
            timestamp = datetime.strptime(
                timestamp_str,
                '%Y-%m-%dT%H:%M:%S.%fZ'
            ).replace(tzinfo=timezone.utc)
            return (datetime.now(timezone.utc) - timestamp).total_seconds() / 3600
        except (ValueError, TypeError) as e:
            self.logger.warning(f"  ⚠️  Invalid timestamp: {e}")
            return None

    def classify_data_freshness(self, age_hours: Optional[float]) -> str:
        """
        Classify data freshness level

        Args:
            age_hours: Age in hours (None = no timestamp)

        Returns:
            Classification:
            - 'FRESH' - < 0.5 hours
            - 'AGING' - 0.5 - 1.0 hours
            - 'STALE' - > 1.0 hours
        """
        if age_hours is None:
            return 'FRESH'  # No timestamp = assume fresh

        if age_hours < self.FRESH_THRESHOLD_HOURS:
            return 'FRESH'
        elif age_hours < self.AGING_THRESHOLD_HOURS:
            return 'AGING'
        else:
            return 'STALE'

    def validate_trade_opportunity_data(
        self,
        buy_record: Dict,
        sell_record: Dict,
        buy_waypoint: str,
        sell_waypoint: str,
        good: str
    ) -> Tuple[bool, str]:
        """
        Validate both buy and sell market data freshness

        Ensures that both sides of a trade opportunity have fresh data
        before including the opportunity in route planning.

        Args:
            buy_record: Buy market data record
            sell_record: Sell market data record
            buy_waypoint: Buy market waypoint symbol
            sell_waypoint: Sell market waypoint symbol
            good: Trade good symbol

        Returns:
            Tuple of (is_valid, reason):
            - (True, "Data fresh") if both markets have fresh data
            - (False, reason) if either market has stale data
        """
        # Check buy market freshness
        if not self.is_market_data_fresh(buy_record, buy_waypoint, good, 'buy'):
            return False, f"Buy market data stale: {buy_waypoint}"

        # Check sell market freshness
        if not self.is_market_data_fresh(sell_record, sell_waypoint, good, 'sell'):
            return False, f"Sell market data stale: {sell_waypoint}"

        return True, "Data fresh"

    def get_stale_markets(
        self,
        market_data: Dict[str, Dict[str, Dict]],
        threshold_hours: Optional[float] = None
    ) -> Dict[str, list]:
        """
        Identify all stale markets in a data set

        Args:
            market_data: Nested dict of {waypoint: {good: {record}}}
            threshold_hours: Custom staleness threshold (default: AGING_THRESHOLD_HOURS)

        Returns:
            Dictionary of {waypoint: [stale_goods]} for all stale markets
        """
        threshold = threshold_hours or self.AGING_THRESHOLD_HOURS
        stale_markets = {}

        for waypoint, goods_data in market_data.items():
            stale_goods = []
            for good, record in goods_data.items():
                timestamp = record.get('last_updated')
                if timestamp:
                    age_hours = self.get_data_age_hours(timestamp)
                    if age_hours and age_hours > threshold:
                        stale_goods.append(good)

            if stale_goods:
                stale_markets[waypoint] = stale_goods

        return stale_markets
