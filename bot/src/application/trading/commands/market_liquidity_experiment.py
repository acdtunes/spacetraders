"""Market Liquidity Experiment Command - Coordinator for multi-ship experiments."""

import logging
import uuid
from dataclasses import dataclass, field
from typing import Dict, List

from pymediatr import Request, RequestHandler

from adapters.secondary.persistence.work_queue_repository import WorkQueueRepository
from ports.outbound.market_repository import IMarketRepository
from application.trading.services.market_selector import MarketSelector


logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class MarketLiquidityExperimentCommand(Request[Dict]):
    """Command to start multi-ship market liquidity experiment."""
    ship_symbols: List[str]
    player_id: int
    system_symbol: str
    iterations_per_batch: int = 3
    batch_size_fractions: List[float] = field(
        default_factory=lambda: [0.1, 0.25, 0.5, 1.0]
    )


class MarketLiquidityExperimentHandler(RequestHandler[MarketLiquidityExperimentCommand, Dict]):
    """Handler for market liquidity experiment - coordinates multi-ship testing."""

    def __init__(
        self,
        market_selector: MarketSelector,
        work_queue_repo: WorkQueueRepository,
        market_repo: IMarketRepository
    ):
        """
        Initialize experiment handler.

        Args:
            market_selector: Service for selecting markets and generating pairs
            work_queue_repo: Work queue repository for populating queue
            market_repo: Market repository for discovering goods
        """
        self._market_selector = market_selector
        self._work_queue = work_queue_repo
        self._market_repo = market_repo

    async def handle(self, request: MarketLiquidityExperimentCommand) -> Dict:
        """
        Coordinator: populate queue and launch workers.

        Args:
            request: Experiment command with ships, system, and parameters

        Returns:
            Dict with run_id, container_ids, total_pairs, ships, goods
        """
        # 1. Generate unique run_id
        run_id = str(uuid.uuid4())

        logger.info(f"Starting liquidity experiment: run_id={run_id}")
        logger.info(f"Fleet: {len(request.ship_symbols)} ships")
        logger.info(f"System: {request.system_symbol}")

        # 2. Discover all trade goods in system
        all_goods = self._discover_goods_in_system(
            request.system_symbol,
            request.player_id
        )

        logger.info(f"Discovered {len(all_goods)} goods")

        # 3. Generate all market pairs
        all_pairs = []

        for good in all_goods:
            # Select representative markets
            markets = self._market_selector.select_representative_markets(
                request.system_symbol,
                good,
                request.player_id
            )

            # Generate pairs
            pairs = self._market_selector.generate_market_pairs(markets, good)
            all_pairs.extend(pairs)

            logger.info(f"  {good}: {len(markets)} markets → {len(pairs)} pairs")

        logger.info(f"Total: {len(all_pairs)} market pairs")

        # 4. Populate work queue
        self._work_queue.enqueue_pairs(run_id, request.player_id, all_pairs)

        logger.info(f"Work queue populated: {len(all_pairs)} PENDING")

        # 5. Create daemon container for each ship
        from configuration.container import get_daemon_client
        daemon = get_daemon_client()

        container_ids = []

        for ship_symbol in request.ship_symbols:
            # Generate unique container ID
            container_id = f"experiment-worker-{ship_symbol.lower()}-{uuid.uuid4().hex[:8]}"

            # Create container
            daemon.create_container({
                'container_id': container_id,
                'player_id': request.player_id,
                'container_type': 'command',
                'config': {
                    'command_type': 'ShipExperimentWorkerCommand',
                    'params': {
                        'run_id': run_id,
                        'ship_symbol': ship_symbol,
                        'player_id': request.player_id,
                        'iterations_per_batch': request.iterations_per_batch,
                        'batch_size_fractions': list(request.batch_size_fractions)
                    }
                },
                'restart_policy': 'no'
            })

            container_ids.append(container_id)
            logger.info(f"Created worker container: {ship_symbol} → {container_id}")

        return {
            'run_id': run_id,
            'container_ids': container_ids,
            'total_pairs': len(all_pairs),
            'ships': len(request.ship_symbols),
            'goods': len(all_goods)
        }

    def _discover_goods_in_system(self, system: str, player_id: int) -> List[str]:
        """
        Get all unique trade goods across all markets in system.

        Args:
            system: System symbol
            player_id: Player ID for market access

        Returns:
            Sorted list of unique good symbols
        """
        markets = self._market_repo.list_markets_in_system(
            system,
            player_id,
            max_age_minutes=None  # Don't filter by age - use all available data
        )

        logger.info(f"Found {len(markets)} markets in {system}")

        goods = set()
        for market in markets:
            for trade_good in market.trade_goods:
                goods.add(trade_good.symbol)

        return sorted(list(goods))
