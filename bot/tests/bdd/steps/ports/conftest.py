"""Fixtures for ports tests"""
import pytest


class TestContext:
    """Context object for sharing state between test steps"""
    def __init__(self):
        self.interface = None


@pytest.fixture
def context():
    """Context fixture for ports tests"""
    return TestContext()
