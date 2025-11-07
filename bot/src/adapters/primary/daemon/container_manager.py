"""Container manager for lifecycle management"""
import asyncio
import logging
from datetime import datetime
from typing import Dict, Optional, Any

from .types import ContainerInfo, ContainerStatus, RestartPolicy
from .command_container import CommandContainer

logger = logging.getLogger(__name__)


class ContainerManager:
    """Manage container lifecycle

    Responsibilities:
    - Create and start containers
    - Stop containers gracefully
    - Handle restart policies
    - Track container state
    """

    def __init__(self, mediator, database):
        """Initialize with mediator and database

        Args:
            mediator: Mediator instance for command execution
            database: Database instance for logging
        """
        self._containers: Dict[str, ContainerInfo] = {}
        self._lock = asyncio.Lock()
        self._mediator = mediator
        self._database = database
        self._container_types = {
            'command': CommandContainer
        }

    async def create_container(
        self,
        container_id: str,
        player_id: int,
        container_type: str,
        config: Dict[str, Any],
        restart_policy: str = "no",
        max_restarts: int = 3
    ) -> ContainerInfo:
        """Create and start container

        Args:
            container_id: Unique container ID
            player_id: Player ID
            container_type: Type of container (e.g., 'command')
            config: Container-specific configuration
            restart_policy: Restart policy ('no', 'on-failure', 'always', 'unless-stopped')
            max_restarts: Maximum restart attempts

        Returns:
            ContainerInfo for created container

        Raises:
            ValueError: If container already exists or type unknown
        """
        async with self._lock:
            if container_id in self._containers:
                raise ValueError(f"Container {container_id} already exists")

            # Get container class
            container_class = self._container_types.get(container_type)
            if not container_class:
                raise ValueError(f"Unknown container type: {container_type}")

            # Create info
            info = ContainerInfo(
                container_id=container_id,
                player_id=player_id,
                container_type=container_type,
                status=ContainerStatus.STARTING,
                restart_policy=RestartPolicy(restart_policy),
                restart_count=0,
                max_restarts=max_restarts,
                config=config,
                task=None,
                logs=[],
                started_at=None,
                stopped_at=None,
                exit_code=None,
                exit_reason=None
            )

            # Create container instance
            container = container_class(
                container_id=container_id,
                player_id=player_id,
                config=config,
                mediator=self._mediator,
                database=self._database
            )

            # Start task
            info.task = asyncio.create_task(self._run_container(info, container))
            info.started_at = datetime.now()

            self._containers[container_id] = info
            logger.info(f"Created container {container_id} (type={container_type})")
            return info

    async def _run_container(self, info: ContainerInfo, container):
        """Run container and handle restart policy

        Args:
            info: Container info to update
            container: Container instance to run
        """
        try:
            await container.start()
            info.exit_code = 0
            logger.info(f"Container {info.container_id} completed successfully")
        except Exception as e:
            info.exit_code = 1
            info.exit_reason = str(e)
            logger.error(f"Container {info.container_id} failed: {e}")
        finally:
            info.stopped_at = datetime.now()
            # Set status to STOPPED after execution completes
            # This will be overridden to STARTING if restart happens
            info.status = ContainerStatus.STOPPED

        # Handle restart if needed (only called on failure)
        if info.exit_code != 0:
            await self._handle_restart(info)

    async def _handle_restart(self, info: ContainerInfo):
        """Handle container restart based on policy

        Args:
            info: Container info
        """
        # Check restart policy
        if info.restart_policy == RestartPolicy.NO:
            logger.info(f"Container {info.container_id}: no restart (policy=NO)")
            return

        if info.restart_policy == RestartPolicy.ON_FAILURE and info.exit_code == 0:
            logger.info(f"Container {info.container_id}: no restart (exit code 0)")
            return

        if info.restart_count >= info.max_restarts:
            logger.warning(
                f"Container {info.container_id} exceeded max restarts "
                f"({info.restart_count}/{info.max_restarts})"
            )
            return

        # Exponential backoff
        wait_time = min(60, 2 ** info.restart_count)
        logger.info(
            f"Restarting container {info.container_id} in {wait_time}s "
            f"(attempt {info.restart_count + 1}/{info.max_restarts})"
        )
        await asyncio.sleep(wait_time)

        info.restart_count += 1
        info.status = ContainerStatus.STARTING

        # Recreate container and restart
        container_class = self._container_types[info.container_type]
        container = container_class(
            container_id=info.container_id,
            player_id=info.player_id,
            config=info.config,
            mediator=self._mediator,
            database=self._database
        )
        info.task = asyncio.create_task(self._run_container(info, container))

    async def stop_container(self, container_id: str):
        """Stop container gracefully

        Args:
            container_id: Container to stop

        Raises:
            ValueError: If container not found
        """
        async with self._lock:
            info = self._containers.get(container_id)
            if not info:
                raise ValueError(f"Container {container_id} not found")

            if info.task and not info.task.done():
                info.status = ContainerStatus.STOPPING
                info.task.cancel()
                try:
                    await info.task
                except asyncio.CancelledError:
                    pass

            info.status = ContainerStatus.STOPPED
            info.stopped_at = datetime.now()
            logger.info(f"Stopped container {container_id}")

    def get_container(self, container_id: str) -> Optional[ContainerInfo]:
        """Get container info

        Args:
            container_id: Container ID

        Returns:
            ContainerInfo or None if not found
        """
        return self._containers.get(container_id)

    def list_containers(self, player_id: Optional[int] = None) -> list:
        """List containers, optionally filtered by player

        Args:
            player_id: Optional player ID to filter by

        Returns:
            List of ContainerInfo objects
        """
        containers = list(self._containers.values())
        if player_id is not None:
            containers = [c for c in containers if c.player_id == player_id]
        return containers

    async def remove_container(self, container_id: str):
        """Remove stopped container

        Args:
            container_id: Container to remove

        Raises:
            ValueError: If container not found or still running
        """
        async with self._lock:
            info = self._containers.get(container_id)
            if not info:
                raise ValueError(f"Container {container_id} not found")

            if info.status not in [ContainerStatus.STOPPED, ContainerStatus.FAILED]:
                raise ValueError(
                    f"Cannot remove running container {container_id} "
                    f"(status={info.status.value})"
                )

            del self._containers[container_id]
            logger.info(f"Removed container {container_id}")
