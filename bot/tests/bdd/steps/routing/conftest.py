"""Shared fixtures for routing tests"""
import pytest
from adapters.secondary.routing.ortools_engine import ORToolsRoutingEngine


@pytest.fixture(scope="session")
def shared_routing_engine():
    """
    Shared routing engine for all routing tests.

    Uses session scope to reuse the same engine instance across all tests,
    allowing the pathfinding cache to provide benefit across multiple scenarios.

    Uses reduced timeouts (1s TSP, 1s VRP) for fast test execution.
    """
    return ORToolsRoutingEngine(tsp_timeout=1, vrp_timeout=1)


@pytest.fixture(scope="function")
def routing_engine(shared_routing_engine):
    """
    Routing engine fixture for individual tests.

    Returns the shared engine but clears its cache before each test
    to ensure test isolation while still allowing cache reuse within
    a single test scenario.
    """
    shared_routing_engine.clear_cache()
    return shared_routing_engine
