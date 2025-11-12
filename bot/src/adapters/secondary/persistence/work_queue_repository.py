"""WorkQueueRepository - Manages experiment work queue with atomic claiming."""

from dataclasses import dataclass
from typing import Optional, List, Dict
from datetime import datetime, timezone
from sqlalchemy import select, update, func
from sqlalchemy.engine import Engine

from .models import experiment_work_queue


@dataclass(frozen=True)
class MarketPair:
    """Represents a market pair for experiment testing."""
    queue_id: Optional[int]
    pair_id: str              # "IRON_ORE:X1-A1:X1-B2"
    good_symbol: str          # "IRON_ORE"
    buy_market: str           # "X1-GZ7-A1"
    sell_market: str          # "X1-GZ7-B2"


class WorkQueueRepository:
    """Repository for managing experiment work queue with atomic operations."""

    def __init__(self, engine: Engine):
        """
        Initialize work queue repository.

        Args:
            engine: SQLAlchemy Engine instance
        """
        self._engine = engine

    def enqueue_pairs(
        self,
        run_id: str,
        player_id: int,
        pairs: List[MarketPair]
    ) -> None:
        """
        Bulk insert all pairs as PENDING.

        Args:
            run_id: Unique experiment run identifier
            player_id: Player ID owning the experiment
            pairs: List of MarketPair objects to enqueue
        """
        with self._engine.begin() as conn:
            now = datetime.now(timezone.utc)

            # Bulk insert using executemany for SQLAlchemy Core
            if pairs:
                stmt = experiment_work_queue.insert()
                conn.execute(
                    stmt,
                    [
                        {
                            'run_id': run_id,
                            'player_id': player_id,
                            'pair_id': pair.pair_id,
                            'good_symbol': pair.good_symbol,
                            'buy_market': pair.buy_market,
                            'sell_market': pair.sell_market,
                            'status': 'PENDING',
                            'created_at': now
                        }
                        for pair in pairs
                    ]
                )

    def claim_next_pair(
        self,
        run_id: str,
        ship_symbol: str
    ) -> Optional[MarketPair]:
        """
        Atomically claim next PENDING pair (FIFO).

        Uses transaction to prevent race conditions. Returns None if queue empty.

        Args:
            run_id: Experiment run ID
            ship_symbol: Ship claiming the work

        Returns:
            MarketPair if available, None if queue empty
        """
        with self._engine.begin() as conn:
            # Find next PENDING pair (FIFO)
            stmt = (
                select(
                    experiment_work_queue.c.queue_id,
                    experiment_work_queue.c.pair_id,
                    experiment_work_queue.c.good_symbol,
                    experiment_work_queue.c.buy_market,
                    experiment_work_queue.c.sell_market
                )
                .where(experiment_work_queue.c.run_id == run_id)
                .where(experiment_work_queue.c.status == 'PENDING')
                .order_by(experiment_work_queue.c.queue_id.asc())
                .limit(1)
            )

            result = conn.execute(stmt).fetchone()

            if not result:
                return None

            queue_id = result[0]
            now = datetime.now(timezone.utc)

            # Atomically claim it
            update_stmt = (
                update(experiment_work_queue)
                .where(experiment_work_queue.c.queue_id == queue_id)
                .values(
                    status='CLAIMED',
                    claimed_by=ship_symbol,
                    claimed_at=now,
                    attempts=experiment_work_queue.c.attempts + 1
                )
            )

            conn.execute(update_stmt)

            return MarketPair(
                queue_id=queue_id,
                pair_id=result[1],
                good_symbol=result[2],
                buy_market=result[3],
                sell_market=result[4]
            )

    def mark_complete(self, queue_id: int) -> None:
        """
        Mark pair as COMPLETED.

        Args:
            queue_id: Queue entry ID to mark complete
        """
        with self._engine.begin() as conn:
            now = datetime.now(timezone.utc)

            stmt = (
                update(experiment_work_queue)
                .where(experiment_work_queue.c.queue_id == queue_id)
                .values(
                    status='COMPLETED',
                    completed_at=now
                )
            )

            conn.execute(stmt)

    def mark_failed(self, queue_id: int, error: str) -> None:
        """
        Mark pair as FAILED with error message.

        Args:
            queue_id: Queue entry ID to mark failed
            error: Error message describing the failure
        """
        with self._engine.begin() as conn:
            now = datetime.now(timezone.utc)

            stmt = (
                update(experiment_work_queue)
                .where(experiment_work_queue.c.queue_id == queue_id)
                .values(
                    status='FAILED',
                    error_message=error,
                    completed_at=now
                )
            )

            conn.execute(stmt)

    def get_queue_status(self, run_id: str) -> Dict[str, int]:
        """
        Get count of pairs by status.

        Args:
            run_id: Experiment run ID

        Returns:
            Dict mapping status -> count
        """
        with self._engine.connect() as conn:
            stmt = (
                select(
                    experiment_work_queue.c.status,
                    func.count()
                )
                .where(experiment_work_queue.c.run_id == run_id)
                .group_by(experiment_work_queue.c.status)
            )

            results = conn.execute(stmt).fetchall()
            return {status: count for status, count in results}

    def get_ship_progress(self, run_id: str) -> Dict[str, int]:
        """
        Get pairs completed per ship.

        Args:
            run_id: Experiment run ID

        Returns:
            Dict mapping ship_symbol -> pairs_completed
        """
        with self._engine.connect() as conn:
            stmt = (
                select(
                    experiment_work_queue.c.claimed_by,
                    func.count()
                )
                .where(experiment_work_queue.c.run_id == run_id)
                .where(experiment_work_queue.c.status == 'COMPLETED')
                .group_by(experiment_work_queue.c.claimed_by)
            )

            results = conn.execute(stmt).fetchall()
            return {ship: count for ship, count in results}
