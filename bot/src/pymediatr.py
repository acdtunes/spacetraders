"""
Simple CQRS/Mediator pattern implementation for Python.

This module provides base classes for implementing the Command Query Responsibility
Segregation (CQRS) pattern using a mediator.
"""
from abc import ABC, abstractmethod
from typing import TypeVar, Generic, Any

# Type variables for request and response types
TRequest = TypeVar('TRequest')
TResponse = TypeVar('TResponse')


class Request(Generic[TResponse], ABC):
    """
    Base class for all requests (commands and queries).

    Inheritors should be dataclasses with frozen=True for immutability.
    The generic type parameter TResponse indicates what type this request returns.

    Example:
        @dataclass(frozen=True)
        class GetUserQuery(Request[User]):
            user_id: int
    """
    pass


class RequestHandler(Generic[TRequest, TResponse], ABC):
    """
    Base class for all request handlers.

    Handlers process requests and return responses of type TResponse.
    They should have a single async handle() method.

    Example:
        class GetUserHandler(RequestHandler[GetUserQuery, User]):
            async def handle(self, request: GetUserQuery) -> User:
                # Handle the request
                pass
    """

    @abstractmethod
    async def handle(self, request: TRequest) -> TResponse:
        """
        Handle the request and return a response.

        Args:
            request: The request to handle

        Returns:
            The response of type TResponse
        """
        pass


class PipelineBehavior(ABC):
    """
    Base class for pipeline behaviors (middleware).

    Behaviors can intercept requests before/after handler execution.
    They form a pipeline where each behavior can:
    - Pre-process the request
    - Call the next behavior/handler
    - Post-process the response
    - Handle exceptions
    """

    @abstractmethod
    async def handle(self, request: Any, next_handler):
        """
        Handle the request and call next in pipeline.

        Args:
            request: The request to handle
            next_handler: Async callable to invoke next behavior/handler

        Returns:
            The response from the pipeline
        """
        pass


class Mediator:
    """
    Mediator implementation with behavior pipeline support.

    The mediator:
    1. Registers handlers for request types
    2. Registers pipeline behaviors (middleware)
    3. Routes requests through behavior pipeline to appropriate handler
    """

    def __init__(self):
        self._handlers = {}
        self._behaviors = []

    def register_handler(self, request_type: type, handler_factory):
        """
        Register a handler factory for a request type.

        Args:
            request_type: The request class to handle
            handler_factory: Callable that returns handler instance
        """
        self._handlers[request_type] = handler_factory

    def register_behavior(self, behavior: PipelineBehavior):
        """
        Register a pipeline behavior (middleware).

        Behaviors are executed in registration order.

        Args:
            behavior: The behavior instance to add to pipeline
        """
        self._behaviors.append(behavior)

    async def send_async(self, request: Request[TResponse]) -> TResponse:
        """
        Send a request through the pipeline to its handler.

        Flow:
        1. Request passes through all registered behaviors
        2. Each behavior can pre/post process
        3. Final handler processes the request
        4. Response bubbles back through behaviors

        Args:
            request: The request to send

        Returns:
            The response from the handler

        Raises:
            ValueError: If no handler registered for request type
        """
        request_type = type(request)

        if request_type not in self._handlers:
            raise ValueError(f"No handler registered for {request_type.__name__}")

        # Build pipeline: behaviors + final handler
        async def final_handler():
            handler = self._handlers[request_type]()
            return await handler.handle(request)

        # Chain behaviors in reverse order so first registered executes first
        pipeline = final_handler
        for behavior in reversed(self._behaviors):
            # Capture current pipeline in closure
            next_pipeline = pipeline
            pipeline = lambda b=behavior, n=next_pipeline: b.handle(request, n)

        # Execute pipeline
        return await pipeline()
