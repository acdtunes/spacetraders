"""Test command that logs at multiple levels for testing container logging"""
import logging
from dataclasses import dataclass
from pymediatr import Request, RequestHandler

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class MultiLevelLoggingCommand(Request[str]):
    """Command that logs at multiple levels for testing

    This command is used in container logging tests to verify that
    different log levels are properly captured and persisted.
    """
    player_id: int
    log_info: bool = True
    log_warning: bool = True
    log_error: bool = True
    log_debug: bool = True


class MultiLevelLoggingCommandHandler(RequestHandler[MultiLevelLoggingCommand, str]):
    """Handler that logs at multiple levels"""

    async def handle(self, request: MultiLevelLoggingCommand) -> str:
        """Log at requested levels

        Args:
            request: Command specifying which log levels to use

        Returns:
            Success message
        """
        if request.log_info:
            logger.info("INFO level test message")
        if request.log_warning:
            logger.warning("WARNING level test message")
        if request.log_error:
            logger.error("ERROR level test message")
        if request.log_debug:
            logger.debug("DEBUG level test message")
        return "Logged at multiple levels"
