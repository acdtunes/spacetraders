"""Base container abstract class"""
import asyncio
import logging
from abc import ABC, abstractmethod
from datetime import datetime
from typing import Any, Dict

from .types import ContainerStatus

logger = logging.getLogger(__name__)


class BaseContainer(ABC):
    """Abstract base class for all containers

    Provides lifecycle management, logging, and cancellation support.
    Subclasses must implement the run() method.
    """

    def __init__(
        self,
        container_id: str,
        player_id: int,
        config: Dict[str, Any],
        mediator: Any,
        database: Any
    ):
        """Initialize container

        Args:
            container_id: Unique container identifier
            player_id: Player ID owning this container
            config: Container configuration dict
            mediator: Mediator instance for sending commands
            database: Database instance for logging
        """
        self.container_id = container_id
        self.player_id = player_id
        self.config = config
        self.mediator = mediator
        self.database = database
        self.cancel_event = asyncio.Event()
        self.status = ContainerStatus.STARTING
        self.iteration = 0
        self.last_result = None

    @abstractmethod
    async def run(self):
        """Main container logic - implemented by subclasses

        This method should:
        - Execute the container's operation
        - Check self.cancel_event.is_set() periodically
        - Update self.iteration and self.last_result as appropriate
        - Call self.log() to record progress
        """
        pass

    async def start(self):
        """Entry point - runs container then cleanup

        This method:
        - Sets status to RUNNING
        - Calls run()
        - Sets status to STOPPED on success
        - Sets status to FAILED on exception
        - Always calls cleanup()
        """
        try:
            self.status = ContainerStatus.RUNNING
            await self.run()
            self.status = ContainerStatus.STOPPED
        except asyncio.CancelledError:
            self.status = ContainerStatus.STOPPED
            self.log("Container cancelled", level="WARNING")
            raise
        except Exception as e:
            self.status = ContainerStatus.FAILED
            self.log(f"Container failed: {e}", level="ERROR")
            logger.error(f"Container {self.container_id} failed: {e}", exc_info=True)
            raise
        finally:
            await self.cleanup()

    async def cleanup(self):
        """Cleanup resources - override in subclasses if needed"""
        pass

    def log(self, message: str, level: str = "INFO"):
        """Add log entry to database

        Args:
            message: Log message to record
            level: Log level (INFO, WARNING, ERROR, DEBUG)
        """
        try:
            self.database.log_to_database(
                container_id=self.container_id,
                player_id=self.player_id,
                message=message,
                level=level
            )
            logger.info(f"[{self.container_id}] [{level}] {message}")
        except Exception as e:
            # Fallback to logger if database write fails
            logger.error(f"Failed to write log to database: {e}")
            logger.info(f"[{self.container_id}] [{level}] {message}")
