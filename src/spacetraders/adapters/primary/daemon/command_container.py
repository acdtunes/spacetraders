"""Command container - executes any CQRS command N times"""
import importlib
import logging

from .base_container import BaseContainer
from .assignment_manager import ShipAssignmentManager

logger = logging.getLogger(__name__)


class ContainerLogHandler(logging.Handler):
    """Custom logging handler that forwards Python logs to container database logs"""

    def __init__(self, container):
        """
        Initialize handler with container reference.

        Args:
            container: Container instance with log() method
        """
        super().__init__()
        self.container = container

    def emit(self, record: logging.LogRecord):
        """
        Forward log record to container's database logging.

        Args:
            record: Python logging record
        """
        try:
            # Get log level name (INFO, WARNING, ERROR, etc.)
            level = record.levelname

            # Format the message
            message = self.format(record)

            # Write directly to database to avoid infinite recursion
            # (container.log() uses logger.info() which would trigger this handler again)
            self.container.database.log_to_database(
                container_id=self.container.container_id,
                player_id=self.container.player_id,
                message=message,
                level=level
            )
        except Exception:
            # Don't let logging errors crash the container
            self.handleError(record)


class CommandContainer(BaseContainer):
    """Execute any CQRS command 1 to N times

    Config format:
        {
            'command_type': 'DockShipCommand' or 'module.path.CommandName',
            'params': {'ship_symbol': 'SHIP-1', 'player_id': 1, ...},
            'iterations': 100  # Optional, defaults to 1
        }
    """

    async def run(self):
        """Execute command N times"""
        command_type = self.config['command_type']
        params = self.config['params']
        iterations = self.config.get('iterations', 1)

        self.log(f"Starting {iterations} iteration(s) of {command_type}")

        # Setup logging handler to capture handler logs
        log_handler = ContainerLogHandler(self)
        log_handler.setLevel(logging.DEBUG)
        root_logger = logging.getLogger()
        root_logger.addHandler(log_handler)

        try:
            # Handle infinite loop (-1 iterations)
            if iterations == -1:
                i = 0
                while not self.cancel_event.is_set():
                    # Build command dynamically
                    command = self._build_command(command_type, params)

                    # Execute via mediator (handler logs will be captured)
                    result = await self.mediator.send_async(command)

                    # Track metrics
                    i += 1
                    self.iteration = i
                    self.last_result = result

                    if i % 10 == 0:
                        self.log(f"Iteration {i} completed (infinite mode)")

                self.log(f"Cancelled after {self.iteration} iteration(s)")
            else:
                # Finite iterations
                for i in range(iterations):
                    if self.cancel_event.is_set():
                        self.log(f"Cancelled at iteration {i+1}/{iterations}")
                        break

                    # Build command dynamically
                    command = self._build_command(command_type, params)

                    # Execute via mediator (handler logs will be captured)
                    result = await self.mediator.send_async(command)

                    # Track metrics
                    self.iteration = i + 1
                    self.last_result = result

                    if (i + 1) % max(1, iterations // 10) == 0 or iterations < 10:
                        self.log(f"Iteration {i+1}/{iterations} completed")

                self.log(f"Completed {self.iteration} iteration(s)")
        finally:
            # Always remove handler to avoid memory leaks
            root_logger.removeHandler(log_handler)

    def _build_command(self, command_type: str, params: dict):
        """Dynamically instantiate command

        Args:
            command_type: Command class name or fully qualified path
            params: Command constructor parameters

        Returns:
            Command instance

        Raises:
            ValueError: If command not found
        """
        # Parse command type
        parts = command_type.split('.')
        if len(parts) > 1:
            # Fully qualified: spacetraders.application.navigation.commands.DockShipCommand
            module_path = '.'.join(parts[:-1])
            class_name = parts[-1]
        else:
            # Just class name - search in known locations
            class_name = command_type
            module_path = self._find_command_module(class_name)

        # Import and instantiate
        module = importlib.import_module(module_path)
        command_class = getattr(module, class_name)
        return command_class(**params)

    def _find_command_module(self, class_name: str) -> str:
        """Search for command in standard locations

        Args:
            class_name: Command class name

        Returns:
            Module path where command was found

        Raises:
            ValueError: If command not found in any standard location
        """
        search_paths = [
            'spacetraders.application.navigation.commands',
            'spacetraders.application.player.commands',
            'spacetraders.application.operations.commands',
            'spacetraders.application.shipyard.commands',
            'spacetraders.application.contracts.commands',
            'spacetraders.application.scouting.commands',
        ]

        for path in search_paths:
            try:
                module = importlib.import_module(path)
                if hasattr(module, class_name):
                    logger.debug(f"Found {class_name} in {path}")
                    return path
            except ImportError:
                continue

        raise ValueError(
            f"Command {class_name} not found in standard locations: {search_paths}"
        )

    async def cleanup(self):
        """Release ship assignment when container stops/fails

        This ensures ships don't get stuck as 'assigned' when containers fail.
        """
        # Extract ship_symbol from command params
        ship_symbol = self.config.get('params', {}).get('ship_symbol')

        if ship_symbol:
            try:
                assignment_mgr = ShipAssignmentManager(self.database)

                # Determine reason based on container status
                if self.status.value == 'FAILED':
                    reason = 'failed'
                elif self.status.value == 'STOPPED':
                    reason = 'stopped'
                else:
                    reason = 'completed'

                assignment_mgr.release(
                    self.player_id,
                    ship_symbol,
                    reason=reason
                )
                self.log(f"Released ship assignment for {ship_symbol}: {reason}")
            except Exception as e:
                logger.error(f"Failed to release ship assignment: {e}")
