"""
Pipeline behaviors (middleware) for the mediator.

Behaviors intercept requests before/after handler execution.
They form a pipeline where each behavior can:
- Pre-process the request
- Call the next behavior/handler
- Post-process the response
- Handle exceptions
"""
import logging
from typing import Any
from pymediatr import PipelineBehavior


logger = logging.getLogger(__name__)


class LoggingBehavior(PipelineBehavior):
    """
    Logs command/query failures for debugging and monitoring.

    Logs:
    - Failure with exception details
    - Re-raises exceptions to preserve error handling

    Success logs are omitted to reduce verbosity (domain-level logs provide
    sufficient context for successful operations).
    """

    async def handle(self, request: Any, next_handler):
        """
        Log request execution failures.

        Args:
            request: The request being processed
            next_handler: Next behavior/handler in pipeline

        Returns:
            Response from handler

        Raises:
            Re-raises any exception from handler after logging
        """
        request_name = type(request).__name__

        try:
            response = await next_handler()
            return response
        except Exception as e:
            logger.error(f"Failed executing {request_name}: {e}", exc_info=True)
            raise


class ValidationBehavior(PipelineBehavior):
    """
    Validates requests before handler execution.

    If the request has a validate() method, calls it.
    This allows requests to define their own validation logic.
    """

    async def handle(self, request: Any, next_handler):
        """
        Validate request if it has validate() method.

        Args:
            request: The request being processed
            next_handler: Next behavior/handler in pipeline

        Returns:
            Response from handler

        Raises:
            ValidationError: If validation fails
        """
        # Call validate() if request has it
        if hasattr(request, 'validate') and callable(getattr(request, 'validate')):
            request.validate()

        # Continue to next behavior/handler
        return await next_handler()


# TransactionBehavior is commented out until we have database transaction support
# class TransactionBehavior(PipelineBehavior):
#     """
#     Wraps handler execution in database transaction.
#
#     Ensures atomicity of operations:
#     - Commits on success
#     - Rolls back on failure
#     """
#
#     def __init__(self, db_context):
#         self._db_context = db_context
#
#     async def handle(self, request: Any, next_handler):
#         """
#         Execute handler within transaction.
#
#         Args:
#             request: The request being processed
#             next_handler: Next behavior/handler in pipeline
#
#         Returns:
#             Response from handler
#
#         Raises:
#             Re-raises any exception after rollback
#         """
#         async with self._db_context.transaction():
#             return await next_handler()
