"""ExperimentRepository - Stores and retrieves experiment transaction results."""

from typing import Dict
from sqlalchemy.engine import Engine

from .models import market_experiments


class ExperimentRepository:
    """Repository for storing and querying experiment transaction results."""

    def __init__(self, engine: Engine):
        """
        Initialize experiment repository.

        Args:
            engine: SQLAlchemy Engine instance
        """
        self._engine = engine

    def record_transaction(self, data: Dict) -> None:
        """
        Store a single experiment transaction.

        Args:
            data: Dictionary with all transaction fields:
                - run_id, player_id, ship_symbol, pair_id
                - good_symbol, buy_market, sell_market
                - operation, iteration, batch_size_fraction
                - units, price_per_unit, total_credits
                - supply_before, supply_after, supply_change
                - activity_before, trade_volume_before
                - price_before, price_after, price_impact_percent
                - timestamp
        """
        with self._engine.begin() as conn:
            conn.execute(
                market_experiments.insert(),
                [data]  # Insert expects a list
            )

    def get_transaction_count(self, run_id: str) -> int:
        """
        Get total transaction count for a run.

        Args:
            run_id: Experiment run ID

        Returns:
            Number of transactions recorded
        """
        from sqlalchemy import select, func

        with self._engine.connect() as conn:
            stmt = (
                select(func.count())
                .select_from(market_experiments)
                .where(market_experiments.c.run_id == run_id)
            )

            result = conn.execute(stmt).scalar()
            return result or 0

    def get_last_transaction_timestamp(
        self,
        market: str,
        good_symbol: str,
        operation: str,
        player_id: int
    ):
        """
        Get timestamp of the last transaction for a specific market+good+operation.

        Args:
            market: Market waypoint symbol
            good_symbol: Good symbol (e.g., "ALUMINUM")
            operation: "BUY" or "SELL"
            player_id: Player ID

        Returns:
            datetime of last transaction, or None if no previous transactions
        """
        from sqlalchemy import select

        with self._engine.connect() as conn:
            # For BUY operations, check buy_market
            # For SELL operations, check sell_market
            market_column = (
                market_experiments.c.buy_market if operation == 'BUY'
                else market_experiments.c.sell_market
            )

            stmt = (
                select(market_experiments.c.timestamp)
                .where(
                    (market_column == market) &
                    (market_experiments.c.good_symbol == good_symbol) &
                    (market_experiments.c.operation == operation) &
                    (market_experiments.c.player_id == player_id)
                )
                .order_by(market_experiments.c.timestamp.desc())
                .limit(1)
            )

            result = conn.execute(stmt).scalar()
            return result
